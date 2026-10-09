package bidgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type stubRunner struct {
	mu          sync.Mutex
	outputs     []Output
	requests    []GenerationRequest
	exports     []string
	generateFn  func(context.Context, *Task, GenerationRequest) (Output, error)
	exportError error
}

func (r *stubRunner) Generate(ctx context.Context, task *Task, request GenerationRequest) (Output, error) {
	r.mu.Lock()
	r.requests = append(r.requests, request)
	if r.generateFn != nil {
		fn := r.generateFn
		r.mu.Unlock()
		return fn(ctx, task, request)
	}
	if len(r.outputs) == 0 {
		r.mu.Unlock()
		return Output{}, errors.New("provider private diagnostic with a secret")
	}
	output := r.outputs[0]
	r.outputs = r.outputs[1:]
	r.mu.Unlock()
	return output, nil
}

func (r *stubRunner) Export(ctx context.Context, task *Task) (*Artifact, error) {
	if r.exportError != nil {
		return nil, r.exportError
	}
	markdown, err := DocumentMarkdown(task)
	if err != nil {
		return nil, err
	}
	r.exports = append(r.exports, markdown)
	return &Artifact{MessageID: task.State.LastMessageID, Index: 0, FileName: "投标文件.docx", Handle: "opaque-handle"}, nil
}

func testStore(t *testing.T) *Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "bidgen.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Task{}))
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX bidgen_active_session ON bid_generation_tasks(tenant_id, session_id) WHERE status NOT IN ('completed','cancelled')").Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return NewStore(db)
}

func fixturePlan(sections int) string {
	plan := Plan{Title: "采购项目投标文件", Facts: "投标企业：核验公司。项目：核验项目。", FormatNotes: "正文宋体小四。", SourceNotes: "本项目招标正文及已核验企业资料。"}
	for i := 0; i < sections; i++ {
		id := string(rune('a' + i))
		if i >= 26 {
			id = fmt.Sprintf("section_%d", i)
		}
		plan.Sections = append(plan.Sections, SectionSpec{ID: id, Title: fmt.Sprintf("第%d章 响应内容", i+1), Requirements: []string{"响应本章采购要求", "说明保障措施"}, TargetWords: 200})
	}
	body, _ := json.Marshal(plan)
	return "```weknora-bid-plan\n" + string(body) + "\n```"
}

func completedSection(id, body, message string) Output {
	return Output{Content: body + "\n\n```weknora-bid-coverage\n{\"section_id\":\"" + id + "\",\"covered_requirements\":[0,1]}\n```\n" + SectionDoneMarker(id), MessageID: message}
}

const substantialBody = "本项目严格按照采购需求组织实施。项目负责人统一协调人员、设备和交付进度，并逐项记录实施结果。验收依据为双方确认的采购技术要求，发现问题及时整改并复核。服务团队提供完整培训、质量保障和后续服务，落实可追溯的履约记录。"

func start(t *testing.T, service *Service) *Task {
	t.Helper()
	task, err := service.Start(t.Context(), StartRequest{TenantID: 7, SessionID: "conversation", UserID: "owner", RequestSnapshot: json.RawMessage(`{"query":"编写完整标书","source":"authorized-tender"}`)})
	require.NoError(t, err)
	return task
}

func load(t *testing.T, store *Store, taskID string) *Task {
	t.Helper()
	task, err := store.Get(t.Context(), taskID)
	require.NoError(t, err)
	return task
}

func scope(task *Task) Scope {
	return Scope{TenantID: task.TenantID, SessionID: task.SessionID, UserID: task.UserID, TaskID: task.ID, ExpectedRevision: task.Revision}
}

func confirmPlan(t *testing.T, service *Service, task *Task) *Task {
	t.Helper()
	require.NoError(t, service.Process(t.Context(), task.ID))
	task = load(t, service.store, task.ID)
	require.Equal(t, StatusAwaitingOutline, task.Status)
	task, err := service.Respond(t.Context(), scope(task), RespondRequest{Action: "confirm_outline"})
	require.NoError(t, err)
	return task
}

func TestFullBidTruncationClarificationRestartAndSavedSectionExport(t *testing.T) {
	// This walk-through pins the single-flight round order (truncate → card →
	// restart), so the step must consume queued outputs one at a time.
	t.Setenv("BIDGEN_PARALLEL_SECTIONS", "1")
	store := testStore(t)
	partial := substantialBody + "\n实施计划：阶段一完成资料核验。"
	card := "请补充到货日期。\n\n```weknora-input\n{\"id\":\"delivery\",\"title\":\"到货日期\",\"questions\":[{\"id\":\"date\",\"label\":\"准确到货日期\",\"type\":\"text\"}]}\n```"
	runner := &stubRunner{outputs: []Output{{Content: fixturePlan(2), MessageID: "outline"}, {Content: partial, Truncated: true, MessageID: "partial"}, {Content: card, MessageID: "need-date"}}}
	var enqueued []string
	service := NewService(store, runner, func(_ context.Context, id string) error { enqueued = append(enqueued, id); return nil })
	task := confirmPlan(t, service, start(t, service))
	require.NoError(t, service.Process(t.Context(), task.ID))
	task = load(t, store, task.ID)
	require.Equal(t, partial, task.State.Sections[0].Content)
	require.False(t, task.State.Sections[0].Completed)
	require.NoError(t, service.Process(t.Context(), task.ID))
	task = load(t, store, task.ID)
	require.Equal(t, StatusAwaitingInput, task.Status)
	require.Equal(t, "need-date", task.State.PendingMessageID)
	require.Equal(t, partial, task.State.Sections[0].Content, "clarification prose is never bid text")
	require.Empty(t, runner.exports)

	// A new process uses only the persisted task, including original source
	// scope and saved draft text; chat context or in-memory cursors are unnecessary.
	continuation := "实施计划：阶段一完成资料核验。阶段二按确认日期完成到货验收，责任人负责记录和问题闭环。"
	runner2 := &stubRunner{outputs: []Output{completedSection("a", continuation, "chapter-a"), completedSection("b", substantialBody, "chapter-b")}}
	service2 := NewService(NewStore(store.db), runner2, nil)
	task, err := service2.Respond(t.Context(), scope(task), RespondRequest{Action: "text", Text: "到货日期为2026年12月1日", MessageID: "reply", RequestSnapshot: json.RawMessage(`{"query":"编写完整标书","source":"authorized-tender","attachment":"approved"}`)})
	require.NoError(t, err)
	require.NoError(t, service2.Process(t.Context(), task.ID))
	task = load(t, store, task.ID)
	require.Equal(t, 1, task.State.SectionIndex)
	require.Equal(t, 1, strings.Count(task.State.Sections[0].Content, "阶段一完成资料核验。"))
	require.True(t, task.State.Sections[0].Completed)
	require.False(t, task.State.Sections[0].Truncated)
	require.Len(t, task.State.Sections[0].Versions, 2)
	require.Contains(t, runner2.requests[0].Prompt, "2026年12月1日")
	require.True(t, runner2.requests[0].Continuation)
	require.NoError(t, service2.Process(t.Context(), task.ID))
	task = load(t, store, task.ID)
	require.Equal(t, PhaseExport, task.State.Phase)
	require.NoError(t, service2.Process(t.Context(), task.ID))
	task = load(t, store, task.ID)
	require.Equal(t, StatusCompleted, task.Status)
	require.Equal(t, "chapter-b", task.State.Export.MessageID)
	require.Len(t, runner2.exports, 1)
	require.Contains(t, runner2.exports[0], partial)
	require.NotContains(t, runner2.exports[0], "请补充")
	require.NotContains(t, runner2.exports[0], "weknora-")
	require.NotContains(t, runner2.exports[0], "WEKNORA_BID_SECTION_DONE")
	require.NoError(t, service2.Process(t.Context(), task.ID), "duplicate completed job must be harmless")
	require.Len(t, runner2.exports, 1)
	require.GreaterOrEqual(t, len(enqueued), 3)
}

func TestEarlyNaturalStopTruncatedFooterAndUncoveredRequirementDoNotFinish(t *testing.T) {
	store := testStore(t)
	truncatedDone := completedSection("a", substantialBody, "truncated")
	truncatedDone.Truncated = true
	wrongCoverage := completedSection("a", "", "missing-coverage")
	wrongCoverage.Content = strings.ReplaceAll(wrongCoverage.Content, "[0,1]", "[0]")
	runner := &stubRunner{outputs: []Output{{Content: fixturePlan(1)}, {Content: substantialBody, MessageID: "natural-stop"}, truncatedDone, wrongCoverage, completedSection("a", "", "done")}}
	service := NewService(store, runner, nil)
	task := confirmPlan(t, service, start(t, service))
	for i := 0; i < 3; i++ {
		require.NoError(t, service.Process(t.Context(), task.ID))
		task = load(t, store, task.ID)
		require.False(t, task.State.Sections[0].Completed)
		require.Equal(t, 0, task.State.SectionIndex)
		require.Empty(t, runner.exports)
	}
	require.NoError(t, service.Process(t.Context(), task.ID))
	task = load(t, store, task.ID)
	require.True(t, task.State.Sections[0].Completed)
	require.Equal(t, PhaseExport, task.State.Phase)
}

func TestNoProgressAndProviderFailuresAreBoundedAndPrivate(t *testing.T) {
	store := testStore(t)
	runner := &stubRunner{outputs: []Output{{Content: fixturePlan(1)}, {}, {}, {}}}
	service := NewService(store, runner, nil)
	task := confirmPlan(t, service, start(t, service))
	for i := 0; i < 3; i++ {
		require.NoError(t, service.Process(t.Context(), task.ID))
	}
	task = load(t, store, task.ID)
	require.Equal(t, StatusFailed, task.Status)
	require.NoError(t, service.Process(t.Context(), task.ID))
	require.Len(t, runner.requests, 4)
	task, err := service.Control(t.Context(), scope(task), "resume")
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		require.NoError(t, service.Process(t.Context(), task.ID))
	}
	task = load(t, store, task.ID)
	require.Equal(t, StatusFailed, task.Status)
	require.NotContains(t, task.LastError, "secret")
	require.Equal(t, 3, task.State.RetryCount)
	require.Empty(t, runner.exports)
}

func TestPauseDuringCallSavesOutputWithoutAdvancingAndResumeDoesNotRegenerate(t *testing.T) {
	store := testStore(t)
	runner := &stubRunner{outputs: []Output{{Content: fixturePlan(1), MessageID: "outline"}}}
	service := NewService(store, runner, nil)
	task := confirmPlan(t, service, start(t, service))
	entered, release := make(chan struct{}), make(chan struct{})
	runner.generateFn = func(ctx context.Context, task *Task, request GenerationRequest) (Output, error) {
		close(entered)
		<-release
		task.State.RequestSnapshot = json.RawMessage(`{"source":"authorized-tender","resolved_format":"宋体"}`)
		return completedSection("a", substantialBody, "saved-current-chunk"), nil
	}
	done := make(chan error, 1)
	go func() { done <- service.Process(t.Context(), task.ID) }()
	<-entered
	task = load(t, store, task.ID)
	task, err := service.Control(t.Context(), scope(task), "pause")
	require.NoError(t, err)
	close(release)
	require.NoError(t, <-done)
	task = load(t, store, task.ID)
	require.Equal(t, StatusPaused, task.Status)
	require.Equal(t, 0, task.State.SectionIndex)
	require.Equal(t, PhaseSection, task.State.Phase)
	require.Equal(t, substantialBody, task.State.Sections[0].Content)
	require.True(t, task.State.Sections[0].Completed)
	require.Contains(t, string(task.State.RequestSnapshot), "resolved_format")
	task, err = service.Control(t.Context(), scope(task), "resume")
	require.NoError(t, err)
	require.NoError(t, service.Process(t.Context(), task.ID))
	task = load(t, store, task.ID)
	require.Equal(t, PhaseExport, task.State.Phase)
	require.Len(t, runner.requests, 2, "completed current section must not call the model again")
	require.NoError(t, service.Process(t.Context(), task.ID))
	require.Equal(t, StatusCompleted, load(t, store, task.ID).Status)
}

func TestSessionOwnershipFreshnessUniqueTaskAndExpiredLeaseRecovery(t *testing.T) {
	store := testStore(t)
	var enqueued []string
	runner := &stubRunner{outputs: []Output{{Content: fixturePlan(1), MessageID: "recovered-outline"}}}
	service := NewService(store, runner, func(_ context.Context, id string) error { enqueued = append(enqueued, id); return nil })
	task := start(t, service)
	_, err := service.Start(t.Context(), StartRequest{TenantID: 7, SessionID: task.SessionID, UserID: "owner", RequestSnapshot: json.RawMessage(`{}`)})
	require.ErrorIs(t, err, ErrActiveTask)
	foreign := scope(task)
	foreign.TenantID = 8
	_, err = service.Control(t.Context(), foreign, "cancel")
	require.ErrorIs(t, err, ErrNotFound)
	current, err := service.GetCurrent(t.Context(), 7, task.SessionID, "another-user")
	require.NoError(t, err)
	require.Nil(t, current)
	claimed, err := store.claim(t.Context(), task.ID, "old-process", workerLease)
	require.NoError(t, err)
	_, err = store.claim(t.Context(), task.ID, "other-process", workerLease)
	require.ErrorIs(t, err, ErrLeaseHeld)
	_, err = service.Control(t.Context(), scope(task), "pause")
	require.ErrorIs(t, err, ErrStaleRevision)
	before := len(enqueued)
	require.NoError(t, service.Recover(t.Context()))
	require.Len(t, enqueued, before)
	expired := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, store.db.Model(&Task{}).Where("id = ?", task.ID).Update("lease_expires_at", expired).Error)
	require.NoError(t, service.Recover(t.Context()))
	require.Len(t, enqueued, before+1)
	require.NoError(t, service.Process(t.Context(), task.ID))
	current = load(t, store, task.ID)
	require.Equal(t, StatusAwaitingOutline, current.Status)
	claimed.State.Plan = &Plan{Title: "old worker overwrite"}
	require.NoError(t, service.commitWorker(t.Context(), claimed, "old-process", PhasePlanning, 0))
	require.Equal(t, "采购项目投标文件", load(t, store, task.ID).State.Plan.Title)

	// Replanning is only allowed before body drafting and requires fresh revision.
	current, err = service.Respond(t.Context(), scope(current), RespondRequest{Action: "text", Text: "请加入设备维护章节"})
	require.NoError(t, err)
	require.Equal(t, StatusPlanning, current.Status)
	require.Nil(t, current.State.Plan)
	require.Empty(t, current.State.Sections)
}

func TestCancelledLiveStepCannotOverwriteCheckpointOrScheduleAnotherCall(t *testing.T) {
	store := testStore(t)
	runner := &stubRunner{outputs: []Output{{Content: fixturePlan(1), MessageID: "outline"}}}
	var enqueued []string
	service := NewService(store, runner, func(_ context.Context, id string) error { enqueued = append(enqueued, id); return nil })
	task := confirmPlan(t, service, start(t, service))
	entered, release := make(chan struct{}), make(chan struct{})
	runner.generateFn = func(context.Context, *Task, GenerationRequest) (Output, error) {
		close(entered)
		<-release
		return completedSection("a", substantialBody, "discard"), nil
	}
	done := make(chan error, 1)
	go func() { done <- service.Process(t.Context(), task.ID) }()
	<-entered
	task = load(t, store, task.ID)
	_, err := service.Control(t.Context(), scope(task), "cancel")
	require.NoError(t, err)
	count := len(enqueued)
	close(release)
	require.NoError(t, <-done)
	task = load(t, store, task.ID)
	require.Equal(t, StatusCancelled, task.Status)
	require.Empty(t, task.State.Sections[0].Content)
	require.Len(t, enqueued, count)
	require.Empty(t, runner.exports)
}

func TestParallelSectionBatchDraftsConcurrentlyAndMergesInOrder(t *testing.T) {
	t.Setenv("BIDGEN_PARALLEL_SECTIONS", "4")
	store := testStore(t)
	var live, peak int32
	var mu sync.Mutex
	runner := &stubRunner{generateFn: func(ctx context.Context, task *Task, request GenerationRequest) (Output, error) {
		if request.Phase == PhasePlanning {
			return Output{Content: fixturePlan(3), MessageID: "outline"}, nil
		}
		current := atomic.AddInt32(&live, 1)
		mu.Lock()
		if current > peak {
			peak = current
		}
		mu.Unlock()
		time.Sleep(40 * time.Millisecond)
		atomic.AddInt32(&live, -1)
		// Refresh the snapshot like the real runner does; one refreshed copy
		// must persist so later steps reuse the tender formatting cache.
		task.State.RequestSnapshot = json.RawMessage(`{"query":"编写完整标书","resolved_format":"宋体四号"}`)
		return completedSection(request.SectionID, substantialBody, "msg-"+request.SectionID), nil
	}}
	service := NewService(store, runner, nil)
	task := confirmPlan(t, service, start(t, service))
	require.NoError(t, service.Process(t.Context(), task.ID))
	task = load(t, store, task.ID)
	require.Equal(t, int32(3), peak, "three sections must draft concurrently in one step")
	for i := range task.State.Sections {
		require.True(t, task.State.Sections[i].Completed, "section %d", i)
		require.Equal(t, substantialBody, task.State.Sections[i].Content, "section %d", i)
	}
	require.Equal(t, PhaseExport, task.State.Phase)
	require.Equal(t, 3, task.State.SectionIndex)
	require.Contains(t, string(task.State.RequestSnapshot), "resolved_format")
	require.NoError(t, service.Process(t.Context(), task.ID))
	require.Equal(t, StatusCompleted, load(t, store, task.ID).Status)
}

func TestParallelBatchCardPausesWhileSiblingSectionsFinish(t *testing.T) {
	t.Setenv("BIDGEN_PARALLEL_SECTIONS", "4")
	store := testStore(t)
	card := "请补充到货日期。\n\n```weknora-input\n{\"id\":\"delivery\",\"title\":\"到货日期\",\"questions\":[{\"id\":\"date\",\"label\":\"准确到货日期\",\"type\":\"text\"}]}\n```"
	runner := &stubRunner{generateFn: func(ctx context.Context, task *Task, request GenerationRequest) (Output, error) {
		if request.Phase == PhasePlanning {
			return Output{Content: fixturePlan(3), MessageID: "outline"}, nil
		}
		if request.SectionID == "b" {
			return Output{Content: card, MessageID: "need-date"}, nil
		}
		return completedSection(request.SectionID, substantialBody, "msg-"+request.SectionID), nil
	}}
	service := NewService(store, runner, nil)
	task := confirmPlan(t, service, start(t, service))
	require.NoError(t, service.Process(t.Context(), task.ID))
	task = load(t, store, task.ID)
	require.Equal(t, StatusAwaitingInput, task.Status)
	require.Equal(t, "need-date", task.State.PendingMessageID)
	require.True(t, task.State.Sections[0].Completed, "sibling before the card still completes")
	require.True(t, task.State.Sections[2].Completed, "sibling after the card still completes")
	require.False(t, task.State.Sections[1].Completed)
	require.Empty(t, task.State.Sections[1].Content, "clarification prose is never bid text")
	require.Equal(t, 1, task.State.SectionIndex, "the cursor stops at the section awaiting input")
	require.Equal(t, PhaseSection, task.State.Phase)
}

func TestParallelBatchPartialFailureKeepsSiblingProgress(t *testing.T) {
	t.Setenv("BIDGEN_PARALLEL_SECTIONS", "4")
	store := testStore(t)
	runner := &stubRunner{generateFn: func(ctx context.Context, task *Task, request GenerationRequest) (Output, error) {
		if request.Phase == PhasePlanning {
			return Output{Content: fixturePlan(2), MessageID: "outline"}, nil
		}
		if request.SectionID == "a" {
			return Output{}, errors.New("provider private diagnostic with a secret")
		}
		return completedSection(request.SectionID, substantialBody, "msg-"+request.SectionID), nil
	}}
	service := NewService(store, runner, nil)
	task := confirmPlan(t, service, start(t, service))
	require.NoError(t, service.Process(t.Context(), task.ID))
	task = load(t, store, task.ID)
	require.Equal(t, StatusRunning, task.Status, "one failed call must not fail the whole batch")
	require.True(t, task.State.Sections[1].Completed)
	require.False(t, task.State.Sections[0].Completed)
	require.True(t, task.State.Sections[0].Truncated)
	require.Equal(t, 0, task.State.SectionIndex)
	require.NotContains(t, task.LastError, "secret")
	// The next step retries only the failed section.
	runner.generateFn = func(ctx context.Context, task *Task, request GenerationRequest) (Output, error) {
		return completedSection(request.SectionID, substantialBody, "retry-"+request.SectionID), nil
	}
	require.NoError(t, service.Process(t.Context(), task.ID))
	task = load(t, store, task.ID)
	require.Equal(t, PhaseExport, task.State.Phase)
	require.True(t, task.State.Sections[0].Completed)
	require.Len(t, runner.requests, 1+2+1, "the retry batch must draft only the unfinished section")
}

func TestParallelSectionsEnvBounds(t *testing.T) {
	require.Equal(t, 4, parallelSections())
	t.Setenv("BIDGEN_PARALLEL_SECTIONS", "1")
	require.Equal(t, 1, parallelSections())
	t.Setenv("BIDGEN_PARALLEL_SECTIONS", "0")
	require.Equal(t, 1, parallelSections())
	t.Setenv("BIDGEN_PARALLEL_SECTIONS", "not-a-number")
	require.Equal(t, 1, parallelSections())
	t.Setenv("BIDGEN_PARALLEL_SECTIONS", "16")
	require.Equal(t, 8, parallelSections())
}

func TestExportScopeMixReplansMixedOutlineAndKeepsReplies(t *testing.T) {
	t.Setenv("BIDGEN_PARALLEL_SECTIONS", "1")
	store := testStore(t)
	runner := &stubRunner{outputs: []Output{{Content: fixturePlan(1), MessageID: "outline"}, completedSection("a", substantialBody, "chapter-a")}}
	service := NewService(store, runner, nil)
	task := confirmPlan(t, service, start(t, service))
	require.NoError(t, service.Process(t.Context(), task.ID))
	task = load(t, store, task.ID)
	require.Equal(t, PhaseExport, task.State.Phase)
	require.True(t, task.State.Sections[0].Completed)

	runner.exportError = &ScopeMixError{Replan: true}
	require.NoError(t, service.Process(t.Context(), task.ID))
	task = load(t, store, task.ID)
	require.Equal(t, StatusPlanning, task.Status)
	require.Equal(t, PhasePlanning, task.State.Phase)
	require.Nil(t, task.State.Plan)
	require.Empty(t, task.State.Sections)
	require.Contains(t, string(task.State.RequestSnapshot), "authorized-tender", "the frozen request scope survives the re-plan")
	require.Contains(t, task.LastError, "分别生成")

	// The corrected planning round produces a fresh single-volume outline.
	runner.generateFn = func(ctx context.Context, task *Task, request GenerationRequest) (Output, error) {
		return Output{Content: fixturePlan(1), MessageID: "outline-2"}, nil
	}
	runner.exportError = nil
	require.NoError(t, service.Process(t.Context(), task.ID))
	task = load(t, store, task.ID)
	require.Equal(t, StatusAwaitingOutline, task.Status)
}

func TestExportScopeMixResetsLeakedSectionsOnly(t *testing.T) {
	t.Setenv("BIDGEN_PARALLEL_SECTIONS", "1")
	store := testStore(t)
	runner := &stubRunner{outputs: []Output{{Content: fixturePlan(2), MessageID: "outline"},
		completedSection("a", substantialBody, "chapter-a"), completedSection("b", substantialBody, "chapter-b")}}
	service := NewService(store, runner, nil)
	task := confirmPlan(t, service, start(t, service))
	require.NoError(t, service.Process(t.Context(), task.ID))
	require.NoError(t, service.Process(t.Context(), task.ID))
	task = load(t, store, task.ID)
	require.Equal(t, PhaseExport, task.State.Phase)

	runner.exportError = &ScopeMixError{Sections: []string{"a"}}
	require.NoError(t, service.Process(t.Context(), task.ID))
	task = load(t, store, task.ID)
	require.Equal(t, StatusRunning, task.Status)
	require.Equal(t, PhaseSection, task.State.Phase)
	require.Equal(t, 0, task.State.SectionIndex)
	require.False(t, task.State.Sections[0].Completed)
	require.Empty(t, task.State.Sections[0].Content)
	require.True(t, task.State.Sections[1].Completed, "clean sibling sections are never reset")
	require.Equal(t, substantialBody, task.State.Sections[1].Content)
	require.Contains(t, task.LastError, "已重置")

	// The redrafted section completes and the document exports.
	runner.generateFn = func(ctx context.Context, task *Task, request GenerationRequest) (Output, error) {
		return completedSection(request.SectionID, substantialBody, "retry-"+request.SectionID), nil
	}
	runner.exportError = nil
	require.NoError(t, service.Process(t.Context(), task.ID))
	task = load(t, store, task.ID)
	require.Equal(t, PhaseExport, task.State.Phase)
	require.True(t, task.State.Sections[0].Completed)
	require.NoError(t, service.Process(t.Context(), task.ID))
	require.Equal(t, StatusCompleted, load(t, store, task.ID).Status)
}

func TestExportScopeMixFailsAfterBoundedRetries(t *testing.T) {
	t.Setenv("BIDGEN_PARALLEL_SECTIONS", "1")
	store := testStore(t)
	runner := &stubRunner{outputs: []Output{{Content: fixturePlan(1), MessageID: "outline"}, completedSection("a", substantialBody, "chapter-a")}}
	service := NewService(store, runner, nil)
	task := confirmPlan(t, service, start(t, service))
	require.NoError(t, service.Process(t.Context(), task.ID))
	task = load(t, store, task.ID)
	require.Equal(t, PhaseExport, task.State.Phase)

	// The model keeps leaking commercial phrasing; recovery must not loop forever.
	runner.exportError = &ScopeMixError{Sections: []string{"a"}}
	runner.generateFn = func(ctx context.Context, task *Task, request GenerationRequest) (Output, error) {
		return completedSection(request.SectionID, substantialBody, "again"), nil
	}
	for i := 0; i < 8; i++ {
		require.NoError(t, service.Process(t.Context(), task.ID))
		task = load(t, store, task.ID)
		if task.Status == StatusFailed {
			break
		}
	}
	require.Equal(t, StatusFailed, task.Status)
	require.Contains(t, task.LastError, "分别生成单册")
}
