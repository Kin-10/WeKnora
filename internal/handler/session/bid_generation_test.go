package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/bidformat"
	"github.com/Tencent/WeKnora/internal/bidgen"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/api"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type bidRunnerSessionStub struct {
	interfaces.SessionService
	generate func(context.Context, *types.QARequest, *event.EventBus) error
	owned    *types.Session
}

func (s *bidRunnerSessionStub) GetOwnedSession(ctx context.Context, id string) (*types.Session, error) {
	if id != s.owned.ID || types.MustTenantIDFromContext(ctx) != s.owned.TenantID {
		return nil, errors.New("not owned")
	}
	return s.owned, nil
}
func (s *bidRunnerSessionStub) AgentQA(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
	return s.generate(ctx, req, bus)
}
func (s *bidRunnerSessionStub) KnowledgeQA(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
	return s.generate(ctx, req, bus)
}

type bidSharedAgentStub struct {
	interfaces.AgentShareService
	agent *types.CustomAgent
}

func (s *bidSharedAgentStub) GetSharedAgentForTenant(context.Context, uint64, types.TenantRole, string, ...uint64) (*types.CustomAgent, error) {
	return s.agent, nil
}

type bidRunnerTenantStub struct{ interfaces.TenantService }

func (s *bidRunnerTenantStub) GetTenantByID(_ context.Context, id uint64) (*types.Tenant, error) {
	return &types.Tenant{ID: id}, nil
}

func bidRunnerFixture(t *testing.T, mode string) (*bidGenerationRunner, *bidgen.Task, *bidRunnerSessionStub, *bidExportMessageStub) {
	t.Helper()
	store := &bidExportMessageStub{documentMessageStub: &documentMessageStub{}}
	service := &bidRunnerSessionStub{owned: &types.Session{ID: "bid-session", TenantID: 42, UserID: "owner"}}
	h := &Handler{sessionService: service, messageService: store}
	snapshot := bidGenerationSnapshot{Query: "编写完整标书", SourceMessageID: "starting-user", Language: "zh-CN",
		CustomAgent: &types.CustomAgent{ID: "bid-agent", TenantID: 42, Config: types.CustomAgentConfig{
			AgentMode: mode, ModelID: "model", SystemPrompt: "FROZEN WORKFLOW", KBSelectionMode: "selected", KnowledgeBases: []string{"shared-kb"}}},
		RequestState: &types.SessionLastRequestState{AgentID: "bid-agent", ModelID: "model"}}
	frozen, err := json.Marshal(snapshot)
	require.NoError(t, err)
	task := &bidgen.Task{ID: "job", TenantID: 42, SessionID: "bid-session", UserID: "owner", Status: bidgen.StatusRunning,
		State: bidgen.State{Phase: bidgen.PhaseSection, RequestSnapshot: frozen}}
	return &bidGenerationRunner{handler: h}, task, service, store
}

func TestBidGenerationPublicProjectionOmitsPrivateSnapshotsDraftsAndSourceGrants(t *testing.T) {
	task := &bidgen.Task{ID: "task", SessionID: "conversation", TenantID: 42, UserID: "private-owner", Revision: 9,
		LeaseOwner: "private-worker", LastError: "可继续生成。", Status: bidgen.StatusAwaitingInput,
		State: bidgen.State{Phase: bidgen.PhaseSection, RequestSnapshot: json.RawMessage(`{"api_key":"PRIVATE_TOKEN","system_prompt":"PRIVATE_PROMPT"}`),
			Plan: &bidgen.Plan{Title: "投标文件", Facts: "PRIVATE_FACTS", SourceNotes: "PRIVATE_SOURCE_IDS"},
			Sections: []bidgen.Section{{SectionSpec: bidgen.SectionSpec{ID: "s1", Title: "第一章"}, Content: "PRIVATE_DRAFT",
				Versions: []bidgen.SectionVersion{{Content: "PRIVATE_OLD_DRAFT"}}}},
			PendingInput: "PRIVATE_CARD_JSON", PendingMessageID: "safe-message",
			UserReplies: []bidgen.UserReply{{Text: "PRIVATE_REPLIES"}}, Export: &bidgen.Artifact{MessageID: "artifact-message", Index: 0, FileName: "投标文件.docx", Handle: "PRIVATE_STORAGE"}}}
	data, err := json.Marshal(publicBidGeneration(task))
	require.NoError(t, err)
	for _, private := range []string{"PRIVATE_", "private-owner", "private-worker", "request_snapshot", "versions", "user_replies", "handle"} {
		require.NotContains(t, string(data), private)
	}
	require.Contains(t, string(data), `"pending_message_id":"safe-message"`)
	require.Contains(t, string(data), `"artifact_message_id":"artifact-message"`)
	require.Contains(t, string(data), `"phase":"writing"`)
	require.NotContains(t, string(data), `"content"`)
}

func TestBidGenerationQuickAnswerWaitsForBothSupportedTerminalEvents(t *testing.T) {
	for _, terminal := range []event.EventType{event.EventChatComplete, event.EventAgentFinalAnswer} {
		t.Run(string(terminal), func(t *testing.T) {
			runner, task, service, store := bidRunnerFixture(t, types.AgentModeQuickAnswer)
			answer := "本节正文在异步返回之后才完整生成。"
			service.generate = func(ctx context.Context, _ *types.QARequest, bus *event.EventBus) error {
				go func() {
					select {
					case <-time.After(15 * time.Millisecond):
					case <-ctx.Done():
						return
					}
					if terminal == event.EventChatComplete {
						_ = bus.Emit(ctx, event.Event{Type: event.EventChatStream, Data: event.ChatData{StreamChunk: answer}})
						_ = bus.Emit(ctx, event.Event{Type: terminal, Data: event.ChatData{Response: answer}})
					} else {
						_ = bus.Emit(ctx, event.Event{Type: terminal, Data: event.AgentFinalAnswerData{Content: answer, Done: false}})
						_ = bus.Emit(ctx, event.Event{Type: terminal, Data: event.AgentFinalAnswerData{Done: true}})
					}
				}()
				return nil
			}
			output, err := runner.Generate(t.Context(), task, bidgen.GenerationRequest{Phase: bidgen.PhaseSection, Prompt: "当前章节"})
			require.NoError(t, err)
			require.Equal(t, answer, output.Content)
			require.False(t, output.Truncated)
			require.Equal(t, answer, store.messages[0].Content)
		})
	}
}

func TestBidGenerationRunnerPreservesRawOutputForControllerButHidesBidProtocolAroundCards(t *testing.T) {
	runner, task, service, store := bidRunnerFixture(t, types.AgentModeSmartReasoning)
	card := "```weknora-input\n{\"id\":\"scope\",\"title\":\"补充信息\",\"questions\":[{\"id\":\"name\",\"label\":\"企业名称\",\"type\":\"text\"}]}\n```"
	answer := "```weknora-bid-plan\n{\"private\":true}\n```\n请补充企业名称。\n" + card
	service.generate = func(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
		require.Contains(t, req.CustomAgent.Config.SystemPrompt, "FROZEN WORKFLOW")
		require.True(t, req.CustomAgent.Config.UserInputEnabled)
		return bus.Emit(ctx, event.Event{Type: event.EventAgentComplete, Data: event.AgentCompleteData{FinalAnswer: answer}})
	}
	output, err := runner.Generate(t.Context(), task, bidgen.GenerationRequest{Phase: bidgen.PhaseSection, Prompt: "当前章节"})
	require.NoError(t, err)
	require.Equal(t, answer, output.Content, "controller must see the actual protocol for pausing")
	require.Contains(t, store.messages[0].Content, card)
	require.NotContains(t, store.messages[0].Content, "weknora-bid-plan")
	var restored bidGenerationSnapshot
	require.NoError(t, json.Unmarshal(task.State.RequestSnapshot, &restored))
	require.Equal(t, "FROZEN WORKFLOW", restored.CustomAgent.Config.SystemPrompt)
}

func TestBidGenerationSharedExecutionPreservesOwnerAndRejectsConfigDrift(t *testing.T) {
	runner, task, service, store := bidRunnerFixture(t, types.AgentModeSmartReasoning)
	var snapshot bidGenerationSnapshot
	require.NoError(t, json.Unmarshal(task.State.RequestSnapshot, &snapshot))
	snapshot.SharedAgentReadOnly, snapshot.CustomAgent.TenantID = true, 777
	current := *snapshot.CustomAgent
	current.Config.SystemPrompt = "NEW PROMPT EDIT"
	frozen, _ := json.Marshal(snapshot)
	task.State.RequestSnapshot = frozen
	runner.handler.agentShareService = &bidSharedAgentStub{agent: &current}
	runner.handler.tenantService = &bidRunnerTenantStub{}
	called := 0
	service.generate = func(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
		called++
		require.Equal(t, uint64(777), types.MustTenantIDFromContext(ctx))
		require.Equal(t, uint64(42), types.CallerFromContext(ctx).TenantID)
		sandboxTenant, _ := types.SandboxTenantIDFromContext(ctx)
		require.Equal(t, uint64(42), sandboxTenant)
		require.True(t, access.HasKBGrant(ctx, "shared-kb", 777, types.OrgRoleViewer))
		require.False(t, access.HasKBGrant(ctx, "another-kb", 777, types.OrgRoleViewer))
		require.True(t, req.SharedAgentReadOnly)
		require.Contains(t, req.CustomAgent.Config.SystemPrompt, "FROZEN WORKFLOW")
		require.NotContains(t, req.CustomAgent.Config.SystemPrompt, "NEW PROMPT EDIT")
		return bus.Emit(ctx, event.Event{Type: event.EventAgentComplete, Data: event.AgentCompleteData{FinalAnswer: "有效的共享空间正文。"}})
	}
	_, err := runner.Generate(t.Context(), task, bidgen.GenerationRequest{Phase: bidgen.PhaseSection})
	require.NoError(t, err)
	require.Equal(t, 1, called)
	current.Config.MCPServices = []string{"changed-mcp"}
	_, err = runner.Generate(t.Context(), task, bidgen.GenerationRequest{Phase: bidgen.PhaseSection})
	require.Error(t, err)
	require.Equal(t, 1, called, "changed shared credentials must not start a model call")
	require.Len(t, store.messages, 1)
}

type bidQueueFailureStub struct{}

func (bidQueueFailureStub) Enqueue(*asynq.Task, ...asynq.Option) (*asynq.TaskInfo, error) {
	return nil, errors.New("PRIVATE_REDIS_PASSWORD diagnostic")
}

func TestBidGenerationQueueFailureKeepsOneRecoverableTaskWithoutLeakingDiagnostic(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "adapter.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&bidgen.Task{}))
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	h := &Handler{}
	service := NewBidGenerationService(db, h, bidQueueFailureStub{})
	task, err := service.Start(t.Context(), bidgen.StartRequest{TenantID: 42, SessionID: "bid-session", UserID: "owner", RequestSnapshot: json.RawMessage(`{"approved":true}`)})
	require.Error(t, err)
	require.NotNil(t, task)
	require.Equal(t, bidgen.StatusFailed, task.Status)
	current, err := service.GetCurrent(t.Context(), 42, "bid-session", "owner")
	require.NoError(t, err)
	require.Equal(t, task.ID, current.ID)
	data, _ := json.Marshal(publicBidGeneration(current))
	require.NotContains(t, string(data), "PRIVATE_REDIS_PASSWORD")
	require.True(t, strings.Contains(string(data), "恢复任务"))
}

func TestBidGenerationAsyncTruncationAndReferenceEventsArePersisted(t *testing.T) {
	runner, task, service, store := bidRunnerFixture(t, types.AgentModeQuickAnswer)
	ref := &types.SearchResult{ID: "chunk-authorized", Content: "授权招标证据。"}
	service.generate = func(ctx context.Context, _ *types.QARequest, bus *event.EventBus) error {
		go func() {
			_ = bus.Emit(ctx, event.Event{Type: event.EventAgentReferences, Data: event.AgentReferencesData{References: types.References{ref}}})
			_ = bus.Emit(ctx, event.Event{Type: event.EventAgentFinalAnswer, Data: event.AgentFinalAnswerData{Content: "已生成但被截断的正文。"}})
			_ = bus.Emit(ctx, event.Event{Type: event.EventAgentFinalAnswer, Data: event.AgentFinalAnswerData{Done: true, Truncated: true}})
		}()
		return nil
	}
	output, err := runner.Generate(t.Context(), task, bidgen.GenerationRequest{Phase: bidgen.PhaseSection})
	require.NoError(t, err)
	require.True(t, output.Truncated)
	require.Equal(t, "已生成但被截断的正文。", output.Content)
	require.Len(t, store.messages[0].KnowledgeReferences, 1)
	require.Equal(t, ref.ID, store.messages[0].KnowledgeReferences[0].ID)
	require.Len(t, store.messages[0].AgentSteps, 1)
	require.True(t, store.messages[0].AgentSteps[0].Truncated)
}

func TestBidGenerationAsyncErrorEndsStepWithoutLeakingProviderDiagnostic(t *testing.T) {
	runner, task, service, _ := bidRunnerFixture(t, types.AgentModeQuickAnswer)
	service.generate = func(ctx context.Context, _ *types.QARequest, bus *event.EventBus) error {
		go func() {
			_ = bus.Emit(ctx, event.Event{Type: event.EventError, Data: event.ErrorData{Error: "PRIVATE_PROVIDER_TOKEN"}})
		}()
		return nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	output, err := runner.Generate(ctx, task, bidgen.GenerationRequest{Phase: bidgen.PhaseSection})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "PRIVATE_PROVIDER_TOKEN")
	require.True(t, output.Truncated)
}

func TestBidGenerationRejectsTooManyAccumulatedAttachmentsBeforeSavingUserMessage(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "attachments.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&bidgen.Task{}))
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	runner, _, _, messages := bidRunnerFixture(t, types.AgentModeSmartReasoning)
	h := runner.handler
	h.bidGeneration = bidgen.NewService(bidgen.NewStore(db), runner, nil)
	h.bidGenerationStore = bidgen.NewStore(db)
	snapshot := bidGenerationSnapshot{CustomAgent: &types.CustomAgent{ID: "agent"}, RequestState: &types.SessionLastRequestState{}, AttachmentIDs: []string{"one", "two", "three", "four"}}
	raw, _ := json.Marshal(snapshot)
	task, err := h.bidGeneration.Start(t.Context(), bidgen.StartRequest{TenantID: 42, SessionID: "bid-session", UserID: "owner", RequestSnapshot: raw})
	require.NoError(t, err)
	require.NoError(t, db.Model(&bidgen.Task{}).Where("id = ?", task.ID).Update("status", bidgen.StatusAwaitingInput).Error)
	h.temporaryDocuments = &tenderTemporaryStub{docs: map[string]*types.TemporaryDocument{
		"one": tenderReady("one", "原始资料"), "two": tenderReady("two", "原始资料"),
		"three": tenderReady("three", "原始资料"), "four": tenderReady("four", "原始资料"),
		"five": {ID: "five", FileName: "新证书.pdf", FileType: "pdf"}, "six": {ID: "six", FileName: "补充证明.pdf", FileType: "pdf"},
	}}
	router := newArtifactTestRouter(h)
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), types.UserIDContextKey, "owner"))
		c.Next()
	})
	router.POST("/sessions/:id/bid-generation/:task_id/respond", h.RespondBidGeneration)
	body := fmt.Sprintf(`{"query":"依据新证书继续","attachment_ids":["five","six"],"expected_revision":%d}`, task.Revision)
	request := httptest.NewRequest(http.MethodPost, "/sessions/bid-session/bid-generation/"+task.ID+"/respond", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	require.Zero(t, messages.createCalls)
	current, err := h.bidGenerationStore.Get(t.Context(), task.ID)
	require.NoError(t, err)
	require.Equal(t, string(raw), string(current.State.RequestSnapshot))
	require.Equal(t, bidgen.StatusAwaitingInput, current.Status)
}

type bidSourceTemporaryStub struct {
	interfaces.TemporaryDocumentService
	docs        map[string]*types.TemporaryDocument
	resolvedIDs []string
}

func (s *bidSourceTemporaryStub) Get(_ context.Context, tenant uint64, session, id string) (*types.TemporaryDocument, error) {
	if tenant != 42 || session != "bid-session" {
		return nil, errors.New("wrong source scope")
	}
	return s.docs[id], nil
}

func (s *bidSourceTemporaryStub) ResolveForPrompt(ctx context.Context, tenant uint64, session string, ids []string, _ string) (*types.TemporaryDocumentPromptResult, error) {
	s.resolvedIDs = append([]string(nil), ids...)
	result := &types.TemporaryDocumentPromptResult{}
	for _, id := range ids {
		doc, err := s.Get(ctx, tenant, session, id)
		if err != nil || doc == nil || doc.Status != types.TemporaryDocumentStatusReady {
			return nil, errors.New("PRIVATE_SOURCE_PARSE_FAILURE")
		}
		result.Attachments = append(result.Attachments, types.MessageAttachment{ID: id, FileName: doc.FileName,
			FileType: doc.FileType, ContentMode: "full", Content: doc.Content})
	}
	return result, nil
}

func bidSourceFixture(t *testing.T, status string) (*Handler, *bidgen.Task, *bidRunnerSessionStub, *bidExportMessageStub, *bidSourceTemporaryStub) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "sources.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&bidgen.Task{}))
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	runner, task, service, messages := bidRunnerFixture(t, types.AgentModeSmartReasoning)
	h := runner.handler
	h.bidGenerationStore = bidgen.NewStore(db)
	h.bidGeneration = bidgen.NewService(h.bidGenerationStore, runner, nil)
	temps := &bidSourceTemporaryStub{docs: map[string]*types.TemporaryDocument{}}
	if status != "" {
		temps.docs["original"] = &types.TemporaryDocument{ID: "original", TenantID: 42, SessionID: "bid-session",
			FileName: "企业资质.pdf", FileType: "pdf", Status: status}
	}
	h.temporaryDocuments = temps
	var snapshot bidGenerationSnapshot
	require.NoError(t, json.Unmarshal(task.State.RequestSnapshot, &snapshot))
	snapshot.AttachmentIDs = []string{"original"}
	snapshot.Attachments = types.MessageAttachments{{ID: "original", FileName: "企业资质.pdf", FileType: "pdf"}}
	snapshot.ExecutionContext.DocumentRequested = true
	task.State.RequestSnapshot, err = json.Marshal(snapshot)
	require.NoError(t, err)
	spec := bidgen.SectionSpec{ID: "qualifications", Title: "企业资质", Requirements: []string{"根据授权证书列明企业资格"}, TargetWords: 300}
	task.State.Plan = &bidgen.Plan{Title: "项目投标文件", Sections: []bidgen.SectionSpec{spec}}
	task.State.Sections = []bidgen.Section{{SectionSpec: spec, Content: "已经保存的本章正文，应在附件重新上传后继续保留，不能用附件提示或问题卡替换。", Version: 1}}
	task.Revision = 1
	require.NoError(t, h.bidGenerationStore.Create(t.Context(), task))
	return h, task, service, messages, temps
}

func TestBidGenerationExpiredAttachmentPausesAndReplacementResumesSavedSection(t *testing.T) {
	h, task, service, messages, temps := bidSourceFixture(t, "")
	oldBody := task.State.Sections[0].Content
	modelCalls := 0
	service.generate = func(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
		modelCalls++
		require.Len(t, req.Attachments, 1)
		require.Equal(t, "replacement", req.Attachments[0].ID)
		return bus.Emit(ctx, event.Event{Type: event.EventAgentComplete, Data: event.AgentCompleteData{FinalAnswer: "依据重新上传并核验的企业证书继续补充资格情况，所有证书编号均来自本次授权资料。"}})
	}
	require.NoError(t, h.bidGeneration.Process(t.Context(), task.ID))
	waiting, err := h.bidGenerationStore.Get(t.Context(), task.ID)
	require.NoError(t, err)
	require.Equal(t, bidgen.StatusAwaitingInput, waiting.Status)
	require.Equal(t, bidgen.PhaseSection, waiting.State.Phase)
	require.Zero(t, waiting.State.SectionIndex)
	require.Equal(t, oldBody, waiting.State.Sections[0].Content)
	require.Zero(t, modelCalls)
	require.Contains(t, waiting.State.PendingInput, "重新上传")
	require.NotContains(t, waiting.State.PendingInput, "PRIVATE_SOURCE")
	require.Len(t, messages.messages, 1)
	require.Equal(t, waiting.State.PendingMessageID, messages.messages[0].ID)
	require.True(t, messages.messages[0].IsCompleted)
	require.False(t, messages.messages[0].ExecutionContext.DocumentRequested)

	temps.docs["replacement"] = &types.TemporaryDocument{ID: "replacement", TenantID: 42, SessionID: "bid-session",
		FileName: "企业资质.pdf", FileType: "pdf", Status: types.TemporaryDocumentStatusReady, Content: "公司具有有效资格证书，证书编号已核验。"}
	router := newArtifactTestRouter(h)
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), types.UserIDContextKey, "owner"))
		c.Next()
	})
	router.POST("/sessions/:id/bid-generation/:task_id/respond", h.RespondBidGeneration)
	body := fmt.Sprintf(`{"query":"已重新上传，请继续当前章节","attachment_ids":["replacement"],"expected_revision":%d}`, waiting.Revision)
	request := httptest.NewRequest(http.MethodPost, "/sessions/bid-session/bid-generation/"+task.ID+"/respond", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	resumed, err := h.bidGenerationStore.Get(t.Context(), task.ID)
	require.NoError(t, err)
	require.Equal(t, bidgen.StatusRunning, resumed.Status)
	require.Empty(t, resumed.State.PendingInput)
	require.Equal(t, oldBody, resumed.State.Sections[0].Content)
	var snapshot bidGenerationSnapshot
	require.NoError(t, json.Unmarshal(resumed.State.RequestSnapshot, &snapshot))
	require.Equal(t, []string{"replacement"}, snapshot.AttachmentIDs)
	require.Len(t, snapshot.Attachments, 1)
	require.Equal(t, "replacement", snapshot.Attachments[0].ID)
	require.NoError(t, h.bidGeneration.Process(t.Context(), task.ID))
	progress, err := h.bidGenerationStore.Get(t.Context(), task.ID)
	require.NoError(t, err)
	require.Equal(t, 1, modelCalls)
	require.Equal(t, []string{"replacement"}, temps.resolvedIDs)
	require.Zero(t, progress.State.SectionIndex, "unfinished section stays at the same durable cursor")
	require.Contains(t, progress.State.Sections[0].Content, oldBody)
	require.Contains(t, progress.State.Sections[0].Content, "重新上传并核验")
	require.NotContains(t, progress.State.Sections[0].Content, "weknora-input")
}

func TestBidGenerationProcessingAttachmentPausesWithoutModelCall(t *testing.T) {
	h, task, service, messages, _ := bidSourceFixture(t, types.TemporaryDocumentStatusProcessing)
	oldBody := task.State.Sections[0].Content
	service.generate = func(context.Context, *types.QARequest, *event.EventBus) error {
		t.Fatal("processing attachment must pause before invoking the model")
		return nil
	}
	require.NoError(t, h.bidGeneration.Process(t.Context(), task.ID))
	waiting, err := h.bidGenerationStore.Get(t.Context(), task.ID)
	require.NoError(t, err)
	require.Equal(t, bidgen.StatusAwaitingInput, waiting.Status)
	require.Equal(t, bidgen.PhaseSection, waiting.State.Phase)
	require.Zero(t, waiting.State.SectionIndex)
	require.Equal(t, oldBody, waiting.State.Sections[0].Content)
	require.Contains(t, waiting.State.PendingInput, "等待附件解析")
	require.NotContains(t, waiting.State.PendingInput, "重新上传")
	require.NotContains(t, waiting.State.PendingInput, "PRIVATE_SOURCE")
	require.Len(t, messages.messages, 1)
	require.False(t, messages.messages[0].ExecutionContext.DocumentRequested)
}


func TestBidGenerationRunnerDisablesThinkingOnlyForSectionDrafting(t *testing.T) {
	runner, task, service, _ := bidRunnerFixture(t, types.AgentModeSmartReasoning)
	// The session explicitly asked for high effort; planning keeps it while
	// section drafting pins it off for speed.
	var snapshot bidGenerationSnapshot
	require.NoError(t, json.Unmarshal(task.State.RequestSnapshot, &snapshot))
	snapshot.RequestState.ReasoningEffort = string(api.ReasoningHigh)
	frozen, err := json.Marshal(snapshot)
	require.NoError(t, err)
	task.State.RequestSnapshot = frozen

	var seenSection *types.QARequest
	service.generate = func(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
		seenSection = req
		return bus.Emit(ctx, event.Event{Type: event.EventAgentComplete, Data: event.AgentCompleteData{FinalAnswer: "正文"}})
	}
	_, err = runner.Generate(t.Context(), task, bidgen.GenerationRequest{Phase: bidgen.PhaseSection, SectionID: "a", Prompt: "当前章节"})
	require.NoError(t, err)
	require.Equal(t, string(api.ReasoningOff), seenSection.ReasoningEffort)
	require.Equal(t, string(api.ReasoningOff), seenSection.CustomAgent.Config.ReasoningEffort)
	require.NotNil(t, seenSection.CustomAgent.Config.Thinking)
	require.False(t, *seenSection.CustomAgent.Config.Thinking)

	// Planning keeps the session-configured level: outline quality is unchanged.
	var seenPlanning *types.QARequest
	service.generate = func(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
		seenPlanning = req
		planBody, _ := json.Marshal(bidgen.Plan{Title: "投标文件",
			Sections: []bidgen.SectionSpec{{ID: "a", Title: "第一章", Requirements: []string{"r"}, TargetWords: 100}}})
		plan := "```weknora-bid-plan\n" + string(planBody) + "\n```"
		return bus.Emit(ctx, event.Event{Type: event.EventAgentComplete, Data: event.AgentCompleteData{FinalAnswer: plan}})
	}
	_, err = runner.Generate(t.Context(), task, bidgen.GenerationRequest{Phase: bidgen.PhasePlanning, Prompt: "规划"})
	require.NoError(t, err)
	require.Equal(t, string(api.ReasoningHigh), seenPlanning.ReasoningEffort)
}

func TestBidGenerationRunnerBansCommercialScopeInAnonymousTechnicalSections(t *testing.T) {
	runner, task, service, _ := bidRunnerFixture(t, types.AgentModeSmartReasoning)
	var snapshot bidGenerationSnapshot
	require.NoError(t, json.Unmarshal(task.State.RequestSnapshot, &snapshot))
	snapshot.ExecutionContext.TenderFormatting = &types.TenderFormattingSnapshot{
		Result: bidformat.Result{Rules: []bidformat.Rule{{
			Scope: bidformat.ScopeTechnical, Property: "anonymous", Value: "true",
		}}},
	}
	frozen, err := json.Marshal(snapshot)
	require.NoError(t, err)
	task.State.RequestSnapshot = frozen

	var systemPrompt string
	service.generate = func(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
		systemPrompt = req.CustomAgent.Config.SystemPrompt
		return bus.Emit(ctx, event.Event{Type: event.EventAgentComplete, Data: event.AgentCompleteData{FinalAnswer: "正文"}})
	}
	_, err = runner.Generate(t.Context(), task, bidgen.GenerationRequest{Phase: bidgen.PhaseSection, SectionID: "a", Prompt: "当前章节"})
	require.NoError(t, err)
	require.Contains(t, systemPrompt, "技术暗标分册")
	require.Contains(t, systemPrompt, "严禁出现")
	require.Contains(t, systemPrompt, "商务标事项，另行编制")

	// Without the anonymous rule the ban must not fire: a commercial volume
	// legitimately discusses pricing.
	snapshot.ExecutionContext.TenderFormatting = nil
	frozen, err = json.Marshal(snapshot)
	require.NoError(t, err)
	task.State.RequestSnapshot = frozen
	service.generate = func(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
		systemPrompt = req.CustomAgent.Config.SystemPrompt
		return bus.Emit(ctx, event.Event{Type: event.EventAgentComplete, Data: event.AgentCompleteData{FinalAnswer: "正文"}})
	}
	_, err = runner.Generate(t.Context(), task, bidgen.GenerationRequest{Phase: bidgen.PhaseSection, SectionID: "a", Prompt: "当前章节"})
	require.NoError(t, err)
	require.NotContains(t, systemPrompt, "技术暗标分册")
}

func TestBidGenerationScopePickerAppearsImmediatelyFromParsedTender(t *testing.T) {
	runner, task, service, store := bidRunnerFixture(t, types.AgentModeSmartReasoning)
	tender := "邯郸市中心血站酶免试剂盒采购项目招标文件：本项目共分为4个包。第1包：乙型肝炎病毒诊断试剂，预算40.7万元；第2包：梅毒螺旋体抗体诊断试剂；第3包：乙肝梅毒试剂；第4包：丙肝艾滋试剂。商务标与技术标分开编制，技术标为暗标。"
	var snapshot bidGenerationSnapshot
	require.NoError(t, json.Unmarshal(task.State.RequestSnapshot, &snapshot))
	snapshot.Query = "请根据招标文件生成完整标书"
	snapshot.Attachments = types.MessageAttachments{{ID: "tender-1", FileName: "招标文件.pdf", FileType: ".pdf", Content: tender, ContentMode: "full"}}
	frozen, err := json.Marshal(snapshot)
	require.NoError(t, err)
	task.State.RequestSnapshot = frozen

	generated := false
	service.generate = func(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
		generated = true
		return bus.Emit(ctx, event.Event{Type: event.EventAgentComplete, Data: event.AgentCompleteData{FinalAnswer: "规划"}})
	}
	output, err := runner.Generate(t.Context(), task, bidgen.GenerationRequest{Phase: bidgen.PhasePlanning, Prompt: "规划"})
	require.NoError(t, err)
	require.False(t, generated, "the deterministic picker must not spend an LLM planning call")
	require.True(t, types.HasUserInputRequest(output.Content))
	require.Contains(t, output.Content, "共检测到 4 个包段")
	require.Contains(t, output.Content, "第1包")
	require.Contains(t, output.Content, "商务标与技术标分册编制")
	require.Contains(t, output.Content, "仅技术标（暗标，正文不含供应商身份与报价）")
	require.Len(t, store.messages, 1)

	// A user reply naming the volume and lot settles the scope: no picker, the
	// planning LLM runs directly.
	task2 := &bidgen.Task{ID: "job-2", TenantID: 42, SessionID: "bid-session", UserID: "owner", Status: bidgen.StatusPlanning,
		State: bidgen.State{Phase: bidgen.PhasePlanning, RequestSnapshot: frozen,
			UserReplies: []bidgen.UserReply{{Text: "补充信息：生成范围：仅技术标（暗标）\n包段：第3包"}}}}
	_, err = runner.Generate(t.Context(), task2, bidgen.GenerationRequest{Phase: bidgen.PhasePlanning, Prompt: "规划"})
	require.NoError(t, err)
	require.True(t, generated, "a scope-settled query goes straight to planning")
}

func TestBidGenerationScopePickerStaysSilentWithoutStructure(t *testing.T) {
	runner, task, service, _ := bidRunnerFixture(t, types.AgentModeSmartReasoning)
	var snapshot bidGenerationSnapshot
	require.NoError(t, json.Unmarshal(task.State.RequestSnapshot, &snapshot))
	snapshot.Query = "请根据招标文件生成完整标书"
	snapshot.Attachments = types.MessageAttachments{{ID: "note-1", FileName: "说明.md", FileType: ".md", Content: "普通项目说明，无分册无包段。", ContentMode: "full"}}
	frozen, err := json.Marshal(snapshot)
	require.NoError(t, err)
	task.State.RequestSnapshot = frozen

	generated := false
	service.generate = func(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
		generated = true
		return bus.Emit(ctx, event.Event{Type: event.EventAgentComplete, Data: event.AgentCompleteData{FinalAnswer: "规划"}})
	}
	_, err = runner.Generate(t.Context(), task, bidgen.GenerationRequest{Phase: bidgen.PhasePlanning, Prompt: "规划"})
	require.NoError(t, err)
	require.True(t, generated, "no detected structure: planning proceeds normally")
}
