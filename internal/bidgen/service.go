package bidgen

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
)

const (
	maxRetries      = 3
	maxSectionCalls = 12
	maxRunCalls     = 240
	workerLease     = 2 * time.Minute
	stepTimeout     = 10 * time.Minute

	defaultParallelSections = 4
	maxParallelSections     = 8
)

// parallelSections returns how many unfinished sections one bounded step may
// draft concurrently. Section prompts are independent — each carries the same
// fixed plan facts plus only its own requirements and saved text — so
// concurrency changes wall-clock time, not what any single call sees.
// BIDGEN_PARALLEL_SECTIONS=1 restores the previous strictly sequential step.
func parallelSections() int {
	raw := strings.TrimSpace(os.Getenv("BIDGEN_PARALLEL_SECTIONS"))
	if raw == "" {
		return defaultParallelSections
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 1
	}
	if value > maxParallelSections {
		return maxParallelSections
	}
	return value
}

var errSchedule = errors.New("bid generation could not be scheduled; resume the saved task to retry")

type Service struct {
	store   *Store
	runner  Runner
	enqueue EnqueueFunc
	active  sync.Map // task ID -> current local step cancellation
}

func NewService(store *Store, runner Runner, enqueue EnqueueFunc) *Service {
	return &Service{store: store, runner: runner, enqueue: enqueue}
}

func validSnapshot(snapshot json.RawMessage) bool {
	return len(snapshot) > 0 && len(snapshot) <= 2*1024*1024 && json.Valid(snapshot) && strings.HasPrefix(strings.TrimSpace(string(snapshot)), "{")
}

func (s *Service) Start(ctx context.Context, request StartRequest) (*Task, error) {
	if request.TenantID == 0 || strings.TrimSpace(request.SessionID) == "" || strings.TrimSpace(request.UserID) == "" || !validSnapshot(request.RequestSnapshot) {
		return nil, ErrInvalidInput
	}
	now := time.Now().UTC()
	task := &Task{ID: uuid.NewString(), TenantID: request.TenantID, SessionID: request.SessionID, UserID: request.UserID,
		Status: StatusPlanning, Revision: 1, State: State{Phase: PhasePlanning, RequestSnapshot: append(json.RawMessage(nil), request.RequestSnapshot...)},
		CreatedAt: now, UpdatedAt: now}
	if err := s.store.Create(ctx, task); err != nil {
		return nil, err
	}
	return task, s.schedule(ctx, task)
}

func (s *Service) GetCurrent(ctx context.Context, tenantID uint64, sessionID, userID string) (*Task, error) {
	if tenantID == 0 || sessionID == "" || userID == "" {
		return nil, ErrInvalidInput
	}
	return s.store.current(ctx, tenantID, sessionID, userID)
}

func (s *Service) scoped(ctx context.Context, scope Scope) (*Task, error) {
	if scope.TaskID == "" || scope.ExpectedRevision <= 0 {
		return nil, ErrInvalidInput
	}
	task, err := s.store.Get(ctx, scope.TaskID)
	if err != nil {
		return nil, err
	}
	if task.TenantID != scope.TenantID || task.SessionID != scope.SessionID || task.UserID != scope.UserID {
		return nil, ErrNotFound
	}
	if task.Revision != scope.ExpectedRevision {
		return nil, ErrStaleRevision
	}
	return task, nil
}

func (s *Service) Respond(ctx context.Context, scope Scope, response RespondRequest) (*Task, error) {
	task, err := s.scoped(ctx, scope)
	if err != nil {
		return nil, err
	}
	if response.RequestSnapshot != nil && !validSnapshot(response.RequestSnapshot) {
		return nil, ErrInvalidInput
	}
	text := strings.TrimSpace(response.Text)
	if len(text) > 48000 {
		return nil, ErrInvalidInput
	}
	switch response.Action {
	case "confirm_outline":
		if task.Status != StatusAwaitingOutline || task.State.Plan == nil {
			return nil, ErrInvalidAction
		}
		task.State.Sections = make([]Section, len(task.State.Plan.Sections))
		for i, spec := range task.State.Plan.Sections {
			task.State.Sections[i].SectionSpec = spec
		}
		task.State.SectionIndex, task.State.Phase, task.Status = 0, PhaseSection, StatusRunning
	case "text":
		if text == "" || (task.Status != StatusAwaitingInput && task.Status != StatusAwaitingOutline) {
			return nil, ErrInvalidAction
		}
		if task.Status == StatusAwaitingOutline {
			// Outline edits precede drafting. Previously drafted sections are
			// never overwritten by this action because it is not available then.
			task.State.Plan, task.State.Sections = nil, nil
			task.State.SectionIndex, task.State.Phase = 0, PhasePlanning
		}
		task.Status = statusForPhase(task.State.Phase)
	default:
		return nil, ErrInvalidAction
	}
	if text != "" {
		if len(task.State.UserReplies) >= 100 {
			return nil, ErrInvalidInput
		}
		task.State.UserReplies = append(task.State.UserReplies, UserReply{Text: text, MessageID: response.MessageID, CreatedAt: time.Now().UTC()})
	}
	if response.RequestSnapshot != nil {
		task.State.RequestSnapshot = append(json.RawMessage(nil), response.RequestSnapshot...)
	}
	task.State.PendingInput, task.State.PendingMessageID, task.State.ResumeStatus = "", "", ""
	resetRetryWindow(task)
	task.LastError = ""
	if err := s.store.save(ctx, task, scope.ExpectedRevision, nil); err != nil {
		return nil, err
	}
	return task, s.schedule(ctx, task)
}

func (s *Service) Control(ctx context.Context, scope Scope, action string) (*Task, error) {
	task, err := s.scoped(ctx, scope)
	if err != nil {
		return nil, err
	}
	switch action {
	case "pause":
		if task.Status == StatusCompleted || task.Status == StatusCancelled || task.Status == StatusPaused || task.Status == StatusFailed {
			return nil, ErrInvalidAction
		}
		task.State.ResumeStatus, task.Status = task.Status, StatusPaused
		// Keep a live worker lease: its current bounded result can checkpoint
		// onto this paused task, while the next section is never scheduled.
	case "resume":
		if task.Status != StatusPaused && task.Status != StatusFailed {
			return nil, ErrInvalidAction
		}
		task.Status = task.State.ResumeStatus
		if task.Status == "" || task.Status == StatusFailed {
			task.Status = statusForPhase(task.State.Phase)
		}
		task.State.ResumeStatus = ""
		resetRetryWindow(task)
		task.LastError = ""
	case "cancel":
		if task.Status == StatusCompleted || task.Status == StatusCancelled {
			return nil, ErrInvalidAction
		}
		task.Status, task.LeaseOwner, task.LeaseExpiresAt = StatusCancelled, "", nil
	default:
		return nil, ErrInvalidAction
	}
	if err := s.store.save(ctx, task, scope.ExpectedRevision, nil); err != nil {
		return nil, err
	}
	if action == "cancel" {
		if active, ok := s.active.Load(task.ID); ok {
			active.(context.CancelFunc)()
		}
	}
	return task, s.schedule(ctx, task)
}

func resetRetryWindow(task *Task) {
	task.State.RetryCount, task.State.ScopeMixResets, task.State.NoProgressCount, task.State.RunStartCalls = 0, 0, 0, task.State.TotalCalls
	if task.State.SectionIndex < len(task.State.Sections) {
		task.State.Sections[task.State.SectionIndex].Attempts = 0
	}
}

func statusForPhase(phase Phase) Status {
	if phase == PhasePlanning {
		return StatusPlanning
	}
	return StatusRunning
}

func (s *Service) schedule(ctx context.Context, task *Task) error {
	if !runnable(task.Status) || s.enqueue == nil {
		return nil
	}
	if s.enqueue(ctx, task.ID) == nil {
		return nil
	}
	// The durable row survives a queue outage. Explicit resume or recovery
	// can retry without creating another task or losing already saved text.
	revision := task.Revision
	task.Status, task.LastError = StatusFailed, "任务暂时无法排队，请恢复任务后重试。"
	_ = s.store.save(context.WithoutCancel(ctx), task, revision, nil)
	return errSchedule
}

// Recover schedules runnable tasks whose previous process no longer owns a
// live lease. Call at startup and periodically; claiming remains atomic in DB.
func (s *Service) Recover(ctx context.Context) error {
	tasks, err := s.store.recoverable(ctx)
	if err != nil {
		return err
	}
	var first error
	for i := range tasks {
		if err := s.schedule(ctx, &tasks[i]); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Process makes at most one bounded batch of Generate/Export calls — parallel
// for sections — checkpoints the merged result, then schedules the next step.
// Repeated delivery cannot run a second batch: claiming stays atomic in DB.
func (s *Service) Process(ctx context.Context, taskID string) error {
	owner := uuid.NewString()
	task, err := s.store.claim(ctx, taskID, owner, workerLease)
	if errors.Is(err, ErrLeaseHeld) || (err == nil && task == nil) {
		return nil
	}
	if err != nil {
		return err
	}
	originalPhase, originalIndex := task.State.Phase, task.State.SectionIndex
	if !validWorkerState(task) {
		task.Status, task.LastError = StatusFailed, "标书任务的阶段或目录状态不完整，请重新确认任务。"
	} else if task.State.TotalCalls-task.State.RunStartCalls >= maxRunCalls {
		task.Status, task.LastError = StatusFailed, "本轮自动生成已达到调用预算，请检查进度后恢复任务。"
	} else if task.State.Phase == PhaseSection && task.State.SectionIndex < len(task.State.Sections) && task.State.Sections[task.State.SectionIndex].Completed {
		advanceCompleted(task)
	} else {
		stepCtx, cancel := context.WithTimeout(ctx, stepTimeout)
		s.active.Store(task.ID, cancel)
		stopHeartbeat := s.heartbeat(stepCtx, cancel, task.ID, owner)
		if s.store.renew(stepCtx, task.ID, owner, workerLease) == nil {
			s.step(stepCtx, task)
		}
		stopHeartbeat()
		cancel()
		s.active.Delete(task.ID)
	}
	if err := s.commitWorker(context.WithoutCancel(ctx), task, owner, originalPhase, originalIndex); err != nil {
		return err
	}
	return s.schedule(context.WithoutCancel(ctx), task)
}

func (s *Service) heartbeat(ctx context.Context, cancel context.CancelFunc, taskID, owner string) func() {
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if s.store.renew(ctx, taskID, owner, workerLease) != nil {
					cancel()
					return
				}
			}
		}
	}()
	return func() { close(stop); <-done }
}

func (s *Service) commitWorker(ctx context.Context, task *Task, owner string, phase Phase, index int) error {
	for attempt := 0; attempt < 3; attempt++ {
		current, err := s.store.Get(ctx, task.ID)
		if err != nil {
			return err
		}
		if current.Status == StatusCancelled || current.LeaseOwner != owner {
			*task = *current
			return nil // Cancelled/reclaimed steps must never overwrite a newer worker.
		}
		if current.State.Phase != phase || current.State.SectionIndex != index {
			return ErrStaleRevision
		}
		if current.Status == StatusPaused {
			resume := task.Status
			// Finish saving this chunk but leave the cursor at the interrupted
			// section. Resume advances a completed section without regenerating it.
			if phase == PhaseSection {
				task.State.SectionIndex, task.State.Phase = index, phase
			}
			task.State.ResumeStatus, task.Status = resume, StatusPaused
		}
		task.LeaseOwner, task.LeaseExpiresAt = "", nil
		if err := s.store.save(ctx, task, current.Revision, &owner); !errors.Is(err, ErrStaleRevision) {
			return err
		}
	}
	return ErrStaleRevision
}

func (s *Service) step(ctx context.Context, task *Task) {
	if task.State.Phase == PhaseExport {
		if task.State.Export != nil {
			task.Status = StatusCompleted
			return
		}
		artifact, err := s.runner.Export(ctx, task)
		if err != nil || artifact == nil || artifact.MessageID == "" || artifact.FileName == "" || artifact.Index < 0 {
			var mix *ScopeMixError
			if errors.As(err, &mix) {
				s.recoverScopeMix(task, mix)
				return
			}
			s.retry(task, "文档汇编失败，请恢复任务后重试。")
			return
		}
		task.State.Export, task.Status, task.LastError = artifact, StatusCompleted, ""
		return
	}
	if task.State.Phase == PhaseSection {
		s.stepSections(ctx, task)
		return
	}
	request := generationRequest(task)
	output, err := s.runner.Generate(ctx, task, request)
	task.State.TotalCalls++
	if output.MessageID != "" {
		task.State.LastMessageID = output.MessageID
	}
	if types.HasUserInputRequest(output.Content) {
		task.State.PendingInput, task.State.PendingMessageID = output.Content, output.MessageID
		task.Status, task.LastError = StatusAwaitingInput, ""
		return
	}
	if err != nil {
		s.retry(task, "本次生成失败，请恢复任务后重试。")
		return
	}
	plan, err := ParsePlan(output.Content)
	if err != nil || output.Truncated {
		s.retry(task, "目录未能完整生成，请调整说明后恢复任务。")
		return
	}
	task.State.Plan, task.Status, task.LastError = plan, StatusAwaitingOutline, ""
	task.State.RetryCount, task.State.NoProgressCount = 0, 0
}

// stepSections drafts up to parallelSections() unfinished sections with
// concurrent bounded Generate calls, then merges every result in outline
// order. Merging, clarification cards, the per-section coverage protocol and
// the document size cap are unchanged from the sequential step: only
// wall-clock time differs. Each goroutine receives a shallow Task copy
// because the runner refreshes the request snapshot of the task it is handed;
// nothing else mutates shared state until the batch joins.
func (s *Service) stepSections(ctx context.Context, task *Task) {
	batch := make([]int, 0, parallelSections())
	for i := task.State.SectionIndex; i < len(task.State.Sections) && len(batch) < cap(batch); i++ {
		if !task.State.Sections[i].Completed {
			batch = append(batch, i)
		}
	}
	if len(batch) == 0 {
		task.Status, task.LastError = StatusFailed, "标书章节状态不完整，请恢复任务后重试。"
		return
	}
	outputs := make([]Output, len(batch))
	errs := make([]error, len(batch))
	runnerTasks := make([]*Task, len(batch))
	var wg sync.WaitGroup
	for n, index := range batch {
		request := sectionRequest(task, index)
		runnerTask := *task
		runnerTasks[n] = &runnerTask
		wg.Add(1)
		go func(n int, taskArg *Task, request GenerationRequest) {
			defer wg.Done()
			outputs[n], errs[n] = s.runner.Generate(ctx, taskArg, request)
		}(n, runnerTasks[n], request)
	}
	wg.Wait()
	// The runner refreshes each copy's request snapshot (tender formatting
	// cache); persist the first refreshed one so later steps reuse it instead
	// of re-parsing the tender on every call.
	for _, runnerTask := range runnerTasks {
		if string(runnerTask.State.RequestSnapshot) != string(task.State.RequestSnapshot) {
			task.State.RequestSnapshot = runnerTask.State.RequestSnapshot
			break
		}
	}

	card, failed, progressed := -1, 0, false
	for n, index := range batch {
		output, err := outputs[n], errs[n]
		task.State.TotalCalls++
		if output.MessageID != "" {
			task.State.LastMessageID = output.MessageID
		}
		section := &task.State.Sections[index]
		section.Attempts++
		prior := section.Content
		section.Content = MergeContinuation(prior, draftContent(output.Content))
		section.Truncated = section.Truncated || output.Truncated || err != nil
		section.LastMessageID = output.MessageID
		if section.Content != prior {
			section.Version++
			section.Versions = append(section.Versions, SectionVersion{Version: section.Version, Content: section.Content, MessageID: output.MessageID, CreatedAt: time.Now().UTC()})
			progressed = true
		}
		if totalContentBytes(task.State.Sections) > maxDocumentBytes {
			task.Status, task.LastError = StatusFailed, "正文超过当前文档汇编大小限制，请缩减目录或章节内容。"
			return
		}
		if err != nil {
			failed++
		}
		if card < 0 && types.HasUserInputRequest(output.Content) {
			card = n
		}
	}
	// Completion stays per section: a chapter only counts as done when its own
	// output passed the coverage protocol, exactly like the sequential step.
	completedAny := false
	for n, index := range batch {
		section := &task.State.Sections[index]
		if !section.Completed && sectionComplete(outputs[n], *section) {
			section.Completed, section.Truncated = true, false
			completedAny = true
		}
	}
	if completedAny {
		task.State.RetryCount, task.State.NoProgressCount, task.LastError = 0, 0, ""
	} else if progressed {
		task.State.NoProgressCount = 0
	} else {
		task.State.NoProgressCount++
	}
	advanceCompleted(task)
	if card >= 0 {
		task.State.PendingInput, task.State.PendingMessageID = outputs[card].Content, outputs[card].MessageID
		task.Status, task.LastError = StatusAwaitingInput, ""
		return
	}
	if failed == len(batch) {
		s.retry(task, "本次生成失败，请恢复任务后重试。")
		return
	}
	for _, index := range batch {
		section := task.State.Sections[index]
		if !section.Completed && (task.State.NoProgressCount >= maxRetries || section.Attempts >= maxSectionCalls) {
			task.Status, task.LastError = StatusFailed, "当前章节未能完整结束，已保存正文；请检查后恢复生成。"
			return
		}
	}
}

func validWorkerState(task *Task) bool {
	if !validSnapshot(task.State.RequestSnapshot) {
		return false
	}
	if task.State.Phase == PhasePlanning {
		return task.Status == StatusPlanning
	}
	if task.Status != StatusRunning || task.State.Plan == nil || len(task.State.Sections) == 0 || len(task.State.Sections) != len(task.State.Plan.Sections) {
		return false
	}
	if task.State.Phase == PhaseSection {
		return task.State.SectionIndex >= 0 && task.State.SectionIndex < len(task.State.Sections)
	}
	return task.State.Phase == PhaseExport && task.State.SectionIndex == len(task.State.Sections)
}

func totalContentBytes(sections []Section) int {
	total := 0
	for _, section := range sections {
		total += len(section.Content)
	}
	return total
}

// advanceCompleted moves the cursor past every consecutively completed
// section. Parallel batches can finish later chapters before earlier ones;
// an unfinished section always stops the cursor, so no chapter is skipped.
func advanceCompleted(task *Task) {
	for task.State.SectionIndex < len(task.State.Sections) && task.State.Sections[task.State.SectionIndex].Completed {
		task.State.SectionIndex++
	}
	if task.State.Phase != PhaseSection {
		return
	}
	if task.State.SectionIndex >= len(task.State.Sections) {
		task.State.Phase = PhaseExport
	}
	task.Status = StatusRunning
}

func (s *Service) retry(task *Task, failure string) {
	task.State.RetryCount++
	if task.State.RetryCount >= maxRetries {
		task.Status, task.LastError = StatusFailed, failure
	} else {
		task.LastError = "本次生成遇到问题，正在从已保存进度重试。"
	}
}

// recoverScopeMix repairs a mixed-volume draft instead of failing the task:
// outline-level mixing re-plans under the single-volume rules (user replies
// are kept, so the corrected prompt no longer offers a combined volume);
// leaked sections are reset for a redraft under the anonymous drafting ban.
// RetryCount bounds the cycle so a persistent leak still surfaces clearly.
func (s *Service) recoverScopeMix(task *Task, mix *ScopeMixError) {
	// Scope-mix cycles need their own counter: a successful redraft resets
	// RetryCount, which would otherwise let a persistent leak loop forever.
	task.State.ScopeMixResets++
	if task.State.ScopeMixResets >= maxRetries {
		task.Status, task.LastError = StatusFailed,
			"正文反复混入商务身份或报价内容，请改为分别生成单册（仅商务标或仅技术标）后再导出。"
		return
	}
	task.LastError = mix.Error()
	if mix.Replan {
		task.State.Plan, task.State.Sections = nil, nil
		task.State.SectionIndex, task.State.Phase, task.Status = 0, PhasePlanning, StatusPlanning
		task.State.PendingInput, task.State.PendingMessageID, task.State.ResumeStatus = "", "", ""
		return
	}
	ids := make(map[string]bool, len(mix.Sections))
	for _, id := range mix.Sections {
		ids[id] = true
	}
	first := -1
	for i := range task.State.Sections {
		if !ids[task.State.Sections[i].ID] {
			continue
		}
		section := &task.State.Sections[i]
		section.Completed, section.Truncated, section.Content, section.Attempts = false, false, "", 0
		section.LastMessageID = ""
		if first < 0 {
			first = i
		}
	}
	if first < 0 {
		task.State.Plan, task.State.Sections = nil, nil
		task.State.SectionIndex, task.State.Phase, task.Status = 0, PhasePlanning, StatusPlanning
		return
	}
	task.State.SectionIndex, task.State.Phase, task.Status = first, PhaseSection, StatusRunning
}
