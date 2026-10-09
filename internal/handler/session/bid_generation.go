package session

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/bidgen"
	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

// Only server-resolved scopes/configuration are frozen here. The HTTP API never
// accepts an agent configuration, tenant identity, source URL or draft body.
type bidGenerationSnapshot struct {
	Query               string                         `json:"query"`
	RequestState        *types.SessionLastRequestState `json:"request_state"`
	CustomAgent         *types.CustomAgent             `json:"custom_agent"`
	Attachments         types.MessageAttachments       `json:"attachments"`
	AttachmentIDs       []string                       `json:"attachment_ids,omitempty"`
	ExecutionContext    types.MessageExecutionContext  `json:"execution_context"`
	SourceMessageID     string                         `json:"source_message_id"`
	Language            string                         `json:"language"`
	SharedAgentReadOnly bool                           `json:"shared_agent_read_only"`
}

type bidGenerationStartRequest struct {
	Query         string          `json:"query"`
	RequestState  json.RawMessage `json:"request_state"`
	AttachmentIDs []string        `json:"attachment_ids"`
}

type bidGenerationRespondRequest struct {
	Query            string   `json:"query"`
	ConfirmOutline   bool     `json:"confirm_outline"`
	AttachmentIDs    []string `json:"attachment_ids"`
	ExpectedRevision int64    `json:"expected_revision"`
}

func NewBidGenerationService(db *gorm.DB, h *Handler, queue interfaces.TaskEnqueuer) *bidgen.Service {
	store := bidgen.NewStore(db)
	runner := &bidGenerationRunner{handler: h}
	svc := bidgen.NewService(store, runner, func(ctx context.Context, id string) error {
		payload, _ := json.Marshal(map[string]string{"task_id": id})
		_, err := queue.Enqueue(asynq.NewTask(types.TypeBidGeneration, payload),
			asynq.Queue(types.QueueDefault), asynq.MaxRetry(3), asynq.Timeout(15*time.Minute))
		return err
	})
	h.bidGeneration, h.bidGenerationStore = svc, store
	return svc
}

func (h *Handler) bidGenerationOwner(c *gin.Context) (*types.Session, string, error) {
	if h.bidGeneration == nil {
		return nil, "", errors.NewInternalServerError("Bid generation unavailable")
	}
	userID, ok := types.UserIDFromContext(c.Request.Context())
	if !ok || types.IsSyntheticUserID(userID) {
		return nil, "", errors.NewForbiddenError("A user-owned conversation is required")
	}
	owned, err := h.sessionService.GetOwnedSession(c.Request.Context(), paramSessionID(c))
	if err != nil || owned == nil {
		return nil, "", errors.NewNotFoundError("Session not found")
	}
	return owned, userID, nil
}

func (h *Handler) GetBidGeneration(c *gin.Context) {
	owned, user, err := h.bidGenerationOwner(c)
	if err != nil {
		_ = c.Error(err)
		return
	}
	task, err := h.bidGeneration.GetCurrent(c.Request.Context(), owned.TenantID, owned.ID, user)
	if err != nil {
		h.bidGenerationError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": publicBidGeneration(task)})
}

func decodeBidRequestState(raw json.RawMessage, fallback *types.SessionLastRequestState) (*types.SessionLastRequestState, error) {
	if len(raw) == 0 || string(raw) == "null" {
		if fallback == nil {
			return nil, bidgen.ErrInvalidInput
		}
		copy := *fallback
		return &copy, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, bidgen.ErrInvalidInput
	}
	if value := fields["agent_source_tenant_id"]; len(value) > 0 && value[0] == '"' {
		var str string
		if json.Unmarshal(value, &str) != nil {
			return nil, bidgen.ErrInvalidInput
		}
		number, err := strconv.ParseUint(str, 10, 64)
		if err != nil {
			return nil, bidgen.ErrInvalidInput
		}
		fields["agent_source_tenant_id"], _ = json.Marshal(number)
	}
	if len(fields["model_id"]) == 0 {
		fields["model_id"] = fields["summary_model_id"]
	}
	data, _ := json.Marshal(fields)
	var state types.SessionLastRequestState
	if json.Unmarshal(data, &state) != nil {
		return nil, bidgen.ErrInvalidInput
	}
	return &state, nil
}

func (h *Handler) newBidGenerationSnapshot(c *gin.Context, input bidGenerationStartRequest, owned *types.Session) (*bidGenerationSnapshot, error) {
	state, err := decodeBidRequestState(input.RequestState, owned.LastRequestState)
	if err != nil {
		return nil, errors.NewBadRequestError("Select an agent and model before starting")
	}
	if state.AgentID == "" {
		state.AgentID = types.BuiltinSmartReasoningID
	}
	request := CreateKnowledgeQARequest{Query: input.Query, AgentID: state.AgentID,
		AgentEnabled: state.AgentEnabled, AgentSourceTenantID: state.AgentSourceTenantID,
		SummaryModelID: state.ModelID, ReasoningEffort: state.ReasoningEffort,
		KnowledgeBaseIDs: state.KnowledgeBaseIDs, KnowledgeIDs: state.KnowledgeIDs,
		TagIDs: state.TagIDs, MCPServiceIDs: state.MCPServiceIDs, SkillNames: state.SkillNames,
		WebSearchEnabled: state.WebSearchEnabled, AttachmentIDs: input.AttachmentIDs, Channel: "web"}
	for _, mention := range state.MentionedItems {
		request.MentionedItems = append(request.MentionedItems, MentionedItemRequest{
			ID: mention.ID, Name: mention.Name, Type: mention.Type, KBType: mention.KBType,
			KBID: mention.KBID, KBName: mention.KBName, SkillName: mention.SkillName})
	}
	noDocument := false
	request.GenerateDocument = &noDocument
	data, _ := json.Marshal(request)
	// Reuse normal chat's owner, shared-agent, attachment and KB scope checks.
	c.Request.Body = io.NopCloser(bytes.NewReader(data))
	rc, _, err := h.parseQARequest(c, "BidGeneration")
	if err != nil {
		return nil, err
	}
	if rc.customAgent == nil {
		return nil, errors.NewBadRequestError("Selected agent is unavailable")
	}
	state.AgentEnabled = rc.customAgent.IsAgentMode()
	state.LocalBrowserEnabled = false
	state.ModelID = rc.assistantMessage.ModelID
	if rc.sharedAgentReadOnly {
		state.AgentSourceTenantID = rc.customAgent.TenantID
	}
	state.KnowledgeBaseIDs, state.KnowledgeIDs = rc.knowledgeBaseIDs, rc.knowledgeIDs
	state.TagIDs, state.MCPServiceIDs, state.SkillNames = rc.tagIDs, rc.mcpServiceIDs, rc.skillNames
	state.MentionedItems = rc.mentionedItems
	snapshot := &bidGenerationSnapshot{Query: input.Query, RequestState: state, CustomAgent: rc.customAgent,
		Attachments: append(rc.attachments, rc.attachmentMetas...), AttachmentIDs: rc.attachmentIDs,
		ExecutionContext: rc.assistantMessage.ExecutionContext,
		Language:         types.LanguageFromContextOrDefault(rc.ctx), SharedAgentReadOnly: rc.sharedAgentReadOnly}
	snapshot.ExecutionContext.DocumentRequested = false
	return snapshot, nil
}

func (h *Handler) StartBidGeneration(c *gin.Context) {
	owned, user, err := h.bidGenerationOwner(c)
	if err != nil {
		_ = c.Error(err)
		return
	}
	var input bidGenerationStartRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128*1024)
	if c.ShouldBindJSON(&input) != nil || strings.TrimSpace(input.Query) == "" || len(input.Query) > 30000 {
		_ = c.Error(errors.NewBadRequestError("A bid writing request is required"))
		return
	}
	// A live ordinary stream owns the same chat turn; never replace it.
	if h.streamManager != nil {
		if live, _, _ := h.streamManager.GetLiveRun(c.Request.Context(), owned.ID); live != "" {
			c.JSON(http.StatusConflict, gin.H{"success": false, "message": "请等待当前回答结束后再开始完整标书生成。"})
			return
		}
	}
	snapshot, err := h.newBidGenerationSnapshot(c, input, owned)
	if err != nil {
		_ = c.Error(err)
		return
	}
	message, err := h.messageService.CreateMessage(c.Request.Context(), &types.Message{
		ID: uuid.NewString(), SessionID: owned.ID, Role: "user", Content: input.Query,
		IsCompleted: true, Channel: "web", RequestID: uuid.NewString(),
		MentionedItems: snapshot.RequestState.MentionedItems, Attachments: snapshot.Attachments})
	if err != nil {
		_ = c.Error(errors.NewInternalServerError("Could not save writing request"))
		return
	}
	snapshot.SourceMessageID = message.ID
	frozen, _ := json.Marshal(snapshot)
	task, err := h.bidGeneration.Start(c.Request.Context(), bidgen.StartRequest{
		TenantID: owned.TenantID, SessionID: owned.ID, UserID: user, RequestSnapshot: frozen})
	if err != nil && task == nil {
		_ = h.messageService.DeleteMessage(c.Request.Context(), owned.ID, message.ID)
		h.bidGenerationError(c, err)
		return
	}
	_ = h.sessionService.UpdateSessionLastRequestState(c.Request.Context(), owned.ID, snapshot.RequestState)
	if owned.Title == "" || owned.Title == "新会话" {
		owned.Title = "完整标书编写"
		_ = h.sessionService.UpdateSession(c.Request.Context(), owned)
	}
	c.JSON(http.StatusAccepted, gin.H{"success": true, "data": publicBidGeneration(task)})
}

func (h *Handler) RespondBidGeneration(c *gin.Context) {
	owned, user, err := h.bidGenerationOwner(c)
	if err != nil {
		_ = c.Error(err)
		return
	}
	var input bidGenerationRespondRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024)
	if c.ShouldBindJSON(&input) != nil || input.ExpectedRevision < 1 || len(input.Query) > 30000 {
		_ = c.Error(errors.NewBadRequestError("Invalid supplementary information"))
		return
	}
	task, err := h.bidGeneration.GetCurrent(c.Request.Context(), owned.TenantID, owned.ID, user)
	if err != nil || task == nil || task.ID != c.Param("task_id") {
		h.bidGenerationError(c, bidgen.ErrNotFound)
		return
	}
	if task.Revision != input.ExpectedRevision {
		h.bidGenerationError(c, bidgen.ErrStaleRevision)
		return
	}
	if task.Status != bidgen.StatusAwaitingInput && task.Status != bidgen.StatusAwaitingOutline {
		h.bidGenerationError(c, bidgen.ErrInvalidAction)
		return
	}
	if input.ConfirmOutline && task.Status != bidgen.StatusAwaitingOutline {
		h.bidGenerationError(c, bidgen.ErrInvalidAction)
		return
	}
	var snapshot bidGenerationSnapshot
	if json.Unmarshal(task.State.RequestSnapshot, &snapshot) != nil || snapshot.CustomAgent == nil || snapshot.RequestState == nil {
		h.bidGenerationError(c, bidgen.ErrInvalidInput)
		return
	}
	if len(input.AttachmentIDs) > 0 {
		ids, normErr := normalizeTemporaryAttachmentIDs(input.AttachmentIDs)
		if normErr != nil || h.temporaryDocuments == nil {
			h.bidGenerationError(c, bidgen.ErrInvalidInput)
			return
		}
		if err := h.discardUnavailableBidAttachments(c.Request.Context(), task, &snapshot); err != nil {
			_ = c.Error(errors.NewInternalServerError(err.Error()))
			return
		}
		for _, id := range ids {
			doc, getErr := h.temporaryDocuments.Get(c.Request.Context(), owned.TenantID, owned.ID, id)
			if getErr != nil || doc == nil {
				h.bidGenerationError(c, bidgen.ErrInvalidInput)
				return
			}
			if len(snapshot.CustomAgent.Config.SupportedFileTypes) > 0 && !containsFileType(snapshot.CustomAgent.Config.SupportedFileTypes, strings.TrimPrefix(doc.FileType, ".")) {
				h.bidGenerationError(c, bidgen.ErrInvalidInput)
				return
			}
			if !containsString(snapshot.AttachmentIDs, id) {
				snapshot.AttachmentIDs = append(snapshot.AttachmentIDs, id)
				snapshot.Attachments = append(snapshot.Attachments, types.MessageAttachment{ID: id, URL: doc.ResourceRef,
					FileName: doc.FileName, FileType: doc.FileType, FileSize: doc.FileSize})
			}
		}
		if len(snapshot.AttachmentIDs) > maxAttachmentUploadsPerRequest {
			_ = c.Error(errors.NewBadRequestError("标书任务最多支持 5 份对话附件；其他材料可导入知识库后引用。"))
			return
		}
		// New source evidence is parsed by the worker before its next bounded call.
		snapshot.ExecutionContext.TenderFormatting = nil
	}
	query := strings.TrimSpace(input.Query)
	action := "text"
	if input.ConfirmOutline {
		action = "confirm_outline"
		if query == "" {
			query = "确认目录，开始自动编写完整标书。"
		}
	}
	if query == "" && len(input.AttachmentIDs) > 0 {
		query = "请依据本次补充的附件继续编写。"
	}
	if query == "" {
		h.bidGenerationError(c, bidgen.ErrInvalidInput)
		return
	}
	message, err := h.messageService.CreateMessage(c.Request.Context(), &types.Message{ID: uuid.NewString(),
		SessionID: owned.ID, Role: "user", Content: query, IsCompleted: true, Channel: "web", RequestID: uuid.NewString(),
		Attachments: snapshot.Attachments, MentionedItems: snapshot.RequestState.MentionedItems})
	if err != nil {
		_ = c.Error(errors.NewInternalServerError("Could not save supplementary information"))
		return
	}
	frozen, _ := json.Marshal(snapshot)
	task, err = h.bidGeneration.Respond(c.Request.Context(), bidgen.Scope{TenantID: owned.TenantID, SessionID: owned.ID,
		UserID: user, TaskID: task.ID, ExpectedRevision: input.ExpectedRevision},
		bidgen.RespondRequest{Action: action, Text: query, MessageID: message.ID, RequestSnapshot: frozen})
	if err != nil && task == nil {
		_ = h.messageService.DeleteMessage(c.Request.Context(), owned.ID, message.ID)
		h.bidGenerationError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"success": true, "data": publicBidGeneration(task)})
}

func (h *Handler) ControlBidGeneration(c *gin.Context) {
	owned, user, err := h.bidGenerationOwner(c)
	if err != nil {
		_ = c.Error(err)
		return
	}
	var input struct {
		Action           string `json:"action"`
		ExpectedRevision int64  `json:"expected_revision"`
	}
	if c.ShouldBindJSON(&input) != nil || input.ExpectedRevision < 1 {
		h.bidGenerationError(c, bidgen.ErrInvalidInput)
		return
	}
	task, err := h.bidGeneration.Control(c.Request.Context(), bidgen.Scope{TenantID: owned.TenantID, SessionID: owned.ID,
		UserID: user, TaskID: c.Param("task_id"), ExpectedRevision: input.ExpectedRevision}, input.Action)
	if err != nil && task == nil {
		h.bidGenerationError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": publicBidGeneration(task)})
}

func (h *Handler) bidGenerationError(c *gin.Context, err error) {
	status, message := http.StatusInternalServerError, "标书任务暂时无法处理，请稍后重试。"
	switch {
	case stderrors.Is(err, bidgen.ErrNotFound):
		status, message = http.StatusNotFound, "标书任务不存在。"
	case stderrors.Is(err, bidgen.ErrActiveTask):
		status, message = http.StatusConflict, "当前会话已有标书任务，请继续或停止该任务后再开始。"
	case stderrors.Is(err, bidgen.ErrStaleRevision):
		status, message = http.StatusConflict, "任务进度已更新，请刷新后重试。"
	case stderrors.Is(err, bidgen.ErrInvalidInput), stderrors.Is(err, bidgen.ErrInvalidAction):
		status, message = http.StatusBadRequest, "请在当前任务阶段填写或选择有效信息。"
	}
	c.JSON(status, gin.H{"success": false, "message": message})
}

func publicBidGeneration(task *bidgen.Task) interface{} {
	if task == nil {
		return nil
	}
	title, phase := "完整标书", "planning"
	if task.State.Plan != nil {
		title = task.State.Plan.Title
	}
	if task.State.Phase == bidgen.PhaseSection {
		phase = "writing"
	}
	if task.State.Phase == bidgen.PhaseExport {
		phase = "exporting"
	}
	savedSections := task.State.Sections
	if len(savedSections) == 0 && task.State.Plan != nil {
		for _, spec := range task.State.Plan.Sections {
			savedSections = append(savedSections, bidgen.Section{SectionSpec: spec})
		}
	}
	sections := make([]gin.H, 0, len(savedSections))
	total := 0
	for index, section := range savedSections {
		status := "pending"
		if section.Completed {
			status = "completed"
		} else if task.Status == bidgen.StatusRunning && index == task.State.SectionIndex && task.State.Phase == bidgen.PhaseSection {
			status = "writing"
		}
		count := utf8.RuneCountInString(section.Content)
		total += count
		sections = append(sections, gin.H{"id": section.ID, "title": section.Title, "status": status, "word_count": count})
	}
	data := gin.H{"id": task.ID, "session_id": task.SessionID, "status": task.Status, "phase": phase,
		"revision": task.Revision, "title": title, "sections": sections, "current_section": task.State.SectionIndex,
		"total_words": total, "pending_message_id": task.State.PendingMessageID, "error": task.LastError}
	if task.State.Export != nil {
		data["artifact_message_id"], data["artifact_index"], data["artifact_file_name"] = task.State.Export.MessageID, task.State.Export.Index, task.State.Export.FileName
	}
	return data
}

func (h *Handler) bidGenerationBlocksChat(ctx context.Context, sessionID string, tenantID uint64) bool {
	if h.bidGeneration == nil {
		return false
	}
	userID, _ := types.UserIDFromContext(ctx)
	task, err := h.bidGeneration.GetCurrent(ctx, tenantID, sessionID, userID)
	return err == nil && task != nil && (task.Status == bidgen.StatusPlanning || task.Status == bidgen.StatusRunning ||
		task.Status == bidgen.StatusAwaitingInput || task.Status == bidgen.StatusAwaitingOutline)
}

// Wake durable tasks whose trigger was lost, including leases left by a stopped
// process. Active worker leases are respected by Recover and Process.
func RecoverBidGenerationTasks(svc *bidgen.Service, cleaner interfaces.ResourceCleaner) {
	stop := make(chan struct{})
	cleaner.RegisterWithName("BidGenerationRecovery", func() error { close(stop); return nil })
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := svc.Recover(ctx); err != nil {
				logger.Warnf(ctx, "Recover bid generation tasks: %v", err)
			}
			cancel()
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
}

// HandleBidGenerationTask is shared by Redis-backed and Lite workers.
func (h *Handler) HandleBidGenerationTask(ctx context.Context, work *asynq.Task) error {
	var payload struct {
		TaskID string `json:"task_id"`
	}
	if json.Unmarshal(work.Payload(), &payload) != nil || payload.TaskID == "" {
		return fmt.Errorf("invalid bid generation payload: %w", asynq.SkipRetry)
	}
	err := h.bidGeneration.Process(ctx, payload.TaskID)
	if stderrors.Is(err, bidgen.ErrNotFound) || stderrors.Is(err, bidgen.ErrLeaseHeld) {
		return nil
	}
	return err
}
