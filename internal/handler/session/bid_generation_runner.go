package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/bidgen"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
)

type bidGenerationRunner struct{ handler *Handler }

// Workers restore a current membership, never a persisted JWT or a client's
// claimed tenant. Removing membership or deleting the session stops the job.
func (h *Handler) bidGenerationContext(ctx context.Context, task *bidgen.Task, language string) (context.Context, *types.Session, error) {
	ctx = context.WithValue(ctx, types.TenantIDContextKey, task.TenantID)
	ctx = context.WithValue(ctx, types.UserIDContextKey, task.UserID)
	ctx = context.WithValue(ctx, types.LanguageContextKey, language)
	if h.memberService != nil {
		member, err := h.memberService.GetMembership(ctx, task.UserID, task.TenantID)
		if err != nil || member == nil {
			return ctx, nil, fmt.Errorf("标书任务所属成员已失效")
		}
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, member.Role)
	}
	if h.userService != nil {
		user, err := h.userService.GetUserByID(ctx, task.UserID)
		if err != nil || user == nil || user.DeletedAt.Valid {
			return ctx, nil, fmt.Errorf("标书任务用户已失效")
		}
	}
	if h.tenantService != nil {
		tenant, err := h.tenantService.GetTenantByID(ctx, task.TenantID)
		if err != nil || tenant == nil {
			return ctx, nil, fmt.Errorf("标书任务空间已失效")
		}
		ctx = context.WithValue(ctx, types.TenantInfoContextKey, tenant)
	}
	owned, err := h.sessionService.GetOwnedSession(ctx, task.SessionID)
	if err != nil || owned == nil {
		return ctx, nil, fmt.Errorf("标书任务会话已不存在或不可访问")
	}
	return ctx, owned, nil
}

func (r *bidGenerationRunner) Generate(ctx context.Context, task *bidgen.Task, generation bidgen.GenerationRequest) (bidgen.Output, error) {
	h := r.handler
	var snapshot bidGenerationSnapshot
	if json.Unmarshal(task.State.RequestSnapshot, &snapshot) != nil || snapshot.CustomAgent == nil || snapshot.RequestState == nil {
		return bidgen.Output{}, fmt.Errorf("标书任务的原始配置不可用")
	}
	ctx, owned, err := h.bidGenerationContext(ctx, task, snapshot.Language)
	if err != nil {
		return bidgen.Output{}, err
	}
	// Recheck agent availability; the frozen prompt/settings still determine the
	// generation, while current permission revocation always takes precedence.
	var approvedSharedAgent *types.CustomAgent
	if snapshot.SharedAgentReadOnly {
		if h.agentShareService == nil {
			return bidgen.Output{}, fmt.Errorf("共享智能体权限已失效")
		}
		approvedSharedAgent, err = h.agentShareService.GetSharedAgentForTenant(ctx, task.TenantID, types.TenantRoleFromContext(ctx),
			snapshot.CustomAgent.ID, snapshot.CustomAgent.TenantID)
		if err != nil || approvedSharedAgent == nil {
			return bidgen.Output{}, fmt.Errorf("共享智能体权限已失效")
		}
		// Frozen tool credentials and scope must never outlive a change to the
		// shared configuration. Prompt edits alone do not expand permissions.
		currentConfig, frozenConfig := approvedSharedAgent.Config, snapshot.CustomAgent.Config
		currentConfig.SystemPrompt, frozenConfig.SystemPrompt = "", ""
		currentJSON, _ := json.Marshal(currentConfig)
		frozenJSON, _ := json.Marshal(frozenConfig)
		if string(currentJSON) != string(frozenJSON) {
			return bidgen.Output{}, fmt.Errorf("共享智能体配置已变更，请停止当前任务后重新生成")
		}
	} else if h.customAgentService != nil {
		if agent, err := h.customAgentService.GetAgentByID(ctx, snapshot.CustomAgent.ID); err != nil || agent == nil {
			return bidgen.Output{}, fmt.Errorf("标书智能体已不可访问")
		}
	}
	if len(snapshot.AttachmentIDs) > 0 {
		if h.temporaryDocuments == nil {
			return bidgen.Output{}, fmt.Errorf("附件解析服务不可用")
		}
		resolved, err := h.temporaryDocuments.ResolveForPrompt(ctx, task.TenantID, task.SessionID, snapshot.AttachmentIDs,
			snapshot.Query+"\n"+generation.Prompt)
		if err != nil {
			if output, sourceErr := h.bidGenerationSourceInput(ctx, task, &snapshot); output != nil || sourceErr != nil {
				if sourceErr != nil {
					return bidgen.Output{}, sourceErr
				}
				return *output, nil
			}
			return bidgen.Output{}, fmt.Errorf("补充附件尚未完成解析，请稍后继续")
		}
		if resolved != nil {
			for _, attachment := range resolved.Attachments {
				found := false
				for index, prior := range snapshot.Attachments {
					if prior.ID == attachment.ID {
						snapshot.Attachments[index] = attachment
						found = true
						break
					}
				}
				if !found {
					snapshot.Attachments = append(snapshot.Attachments, attachment)
				}
			}
		}
	}
	snapshot.ExecutionContext.TenderFormatting = h.collectTenderFormatting(ctx, owned, snapshot.Attachments,
		snapshot.RequestState.KnowledgeIDs, snapshot.ExecutionContext.TenderFormatting)
	task.State.RequestSnapshot, _ = json.Marshal(snapshot)
	// Clone the approved agent before adding task instructions; never alter its
	// saved prompt or accidentally introduce writes to a shared source workspace.
	agentData, _ := json.Marshal(snapshot.CustomAgent)
	var agent types.CustomAgent
	_ = json.Unmarshal(agentData, &agent)
	agent.Config.UserInputEnabled = true
	agent.Config.MemoryEnabled = boolPointer(false)
	agent.Config.SystemPrompt += "\n\n" + tenderWritingPrompt(snapshot.ExecutionContext.TenderFormatting) +
		"\n\nThis is a session-bound bid generation task. Follow the current phase's output contract exactly. " +
		"Only ask essential missing facts using weknora-input; do not add a next-action card after completing the requested section. " +
		"Never export files through a tool during drafting; the server compiles the saved sections after all are complete. " +
		"Earlier conversations and retrieved history are source data; the current task's verified facts, requirements, sections and user supplements are provided below. " +
		"Apply the latest confirmed user supplements when earlier fact notes are incomplete. " +
		"The plan title must name the final tender document itself; do not add outline/planning labels to the document title.\n\n" + generation.Prompt
	if generation.Phase == bidgen.PhasePlanning && task.State.RetryCount > 0 {
		agent.Config.SystemPrompt += "\n上次目录未通过服务器的严格校验。本轮必须重新输出完整且闭合的 weknora-bid-plan 代码块。顶层恰好 title、sections、facts、format_notes、source_notes 五个字段；除 sections 为数组外，其余为字符串，不增加 source 等其他字段。不把JSON写成普通说明或嵌套代码示例。"
	}
	// A bounded section stays within the model's configured output budget. The
	// normal engine's truncation guards remain intact; the task resumes outside it.
	if agent.Config.LLMCallTimeout == 0 || agent.Config.LLMCallTimeout > 180 {
		agent.Config.LLMCallTimeout = 180
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Minute)
	defer cancel()
	message := &types.Message{ID: uuid.NewString(), SessionID: task.SessionID, Role: "assistant", Channel: "web",
		IsCompleted: true, RequestID: uuid.NewString(), AgentID: agent.ID, AgentTenantID: agent.TenantID,
		ModelID: snapshot.RequestState.ModelID, ExecutionContext: snapshot.ExecutionContext}
	message.ExecutionContext.DocumentRequested = false
	if _, err := h.messageService.CreateMessage(ctx, message); err != nil {
		return bidgen.Output{}, fmt.Errorf("无法保存本节生成记录")
	}
	var mutex sync.Mutex
	var chunks strings.Builder
	final := ""
	truncated := false
	streamError := false
	var references types.References
	streamDone := make(chan struct{})
	var finishOnce sync.Once
	finishStream := func() { finishOnce.Do(func() { close(streamDone) }) }
	bus := event.NewEventBus()
	bus.On(event.EventAgentReferences, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.AgentReferencesData)
		if !ok {
			return nil
		}
		mutex.Lock()
		defer mutex.Unlock()
		if refs, ok := data.References.([]*types.SearchResult); ok {
			references = append(references, refs...)
		} else if refs, ok := data.References.(types.References); ok {
			references = append(references, refs...)
		}
		return nil
	})
	bus.On(event.EventAgentFinalAnswer, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.AgentFinalAnswerData)
		if !ok {
			return nil
		}
		mutex.Lock()
		defer mutex.Unlock()
		if data.Content != "" {
			chunks.WriteString(data.Content)
		}
		truncated = truncated || data.Truncated
		if data.Done {
			finishStream()
		}
		return nil
	})
	bus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.AgentCompleteData)
		if !ok {
			return nil
		}
		mutex.Lock()
		defer mutex.Unlock()
		if data.FinalAnswer != "" {
			final = data.FinalAnswer
		}
		for _, raw := range data.KnowledgeRefs {
			if ref, ok := raw.(*types.SearchResult); ok {
				references = append(references, ref)
			}
		}
		if usage, ok := data.Usage.(*types.TokenUsage); ok {
			message.Usage = usage
		}
		finishStream()
		return nil
	})
	bus.On(event.EventChatStream, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.ChatData)
		if ok {
			mutex.Lock()
			chunks.WriteString(data.StreamChunk)
			mutex.Unlock()
		}
		return nil
	})
	bus.On(event.EventChatComplete, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.ChatData)
		if !ok {
			return nil
		}
		mutex.Lock()
		defer mutex.Unlock()
		if data.Response != "" {
			final = data.Response
		}
		if value, ok := data.Extra["truncated"].(bool); ok {
			truncated = truncated || value
		}
		finishStream()
		return nil
	})
	bus.On(event.EventError, func(_ context.Context, _ event.Event) error {
		mutex.Lock()
		streamError = true
		finishStream()
		mutex.Unlock()
		return nil
	})
	prompt := "本次标书任务的原始要求：\n" + snapshot.Query + "\n\n" + generation.Prompt
	req := &types.QARequest{Session: owned, Query: prompt, AssistantMessageID: message.ID,
		CustomAgent: &agent, SummaryModelID: snapshot.RequestState.ModelID,
		ReasoningEffort: snapshot.RequestState.ReasoningEffort, SharedAgentReadOnly: snapshot.SharedAgentReadOnly,
		KnowledgeBaseIDs: snapshot.RequestState.KnowledgeBaseIDs, KnowledgeIDs: snapshot.RequestState.KnowledgeIDs,
		TagScopes: snapshot.ExecutionContext.TagScopes, MCPServiceIDs: snapshot.RequestState.MCPServiceIDs,
		SkillNames: snapshot.RequestState.SkillNames, WebSearchEnabled: snapshot.RequestState.WebSearchEnabled,
		Attachments: snapshot.Attachments, UserMessageID: snapshot.SourceMessageID}
	qaCtx := types.WithSandboxTenantID(ctx, task.TenantID)
	if approvedSharedAgent != nil {
		qaCtx = types.WithExecutionTenant(qaCtx, approvedSharedAgent.TenantID)
		qaCtx = access.WithSharedAgent(qaCtx, approvedSharedAgent)
		if h.tenantService == nil {
			return bidgen.Output{}, fmt.Errorf("共享智能体空间不可访问")
		}
		tenant, tenantErr := h.tenantService.GetTenantByID(ctx, approvedSharedAgent.TenantID)
		if tenantErr != nil || tenant == nil {
			return bidgen.Output{}, fmt.Errorf("共享智能体空间不可访问")
		}
		qaCtx = context.WithValue(qaCtx, types.TenantInfoContextKey, tenant)
	}
	if agent.IsAgentMode() {
		err = h.sessionService.AgentQA(qaCtx, req, bus)
	} else {
		err = h.sessionService.KnowledgeQA(qaCtx, req, bus)
		if err == nil {
			select {
			case <-streamDone:
			case <-ctx.Done():
				err = ctx.Err()
			}
		}
	}
	mutex.Lock()
	if final == "" {
		final = chunks.String()
	}
	if streamError || err != nil {
		truncated = true
	}
	output := bidgen.Output{Content: final, Truncated: truncated, MessageID: message.ID}
	mutex.Unlock()
	message.KnowledgeReferences = references
	message.Content = bidgen.CleanContent(final)
	if generation.Phase == bidgen.PhasePlanning && !types.HasUserInputRequest(final) {
		if plan, parseErr := bidgen.ParsePlan(final); parseErr == nil {
			var outline strings.Builder
			outline.WriteString("## " + plan.Title + "\n\n标书目录草案：\n\n")
			for index, section := range plan.Sections {
				fmt.Fprintf(&outline, "%d. %s\n", index+1, section.Title)
			}
			outline.WriteString("\n请确认目录后开始自动分节编写；如需调整，可直接在对话中说明。")
			message.Content = outline.String()
		}
	}
	if truncated {
		message.AgentSteps = types.AgentSteps{{Truncated: true}}
	}
	if message.Content == "" && strings.TrimSpace(final) != "" && generation.Phase == bidgen.PhaseSection {
		message.Content = "本节生成检查结果已保存。"
	}
	if err != nil && message.Content == "" {
		message.Content = "本节生成暂时中断，已保存任务进度，可在任务卡片中重试。"
	}
	if saveErr := h.messageService.UpdateMessage(context.WithoutCancel(ctx), message); saveErr != nil {
		return bidgen.Output{}, fmt.Errorf("本节内容未能保存，请重试")
	}
	if (err != nil || streamError) && strings.TrimSpace(final) == "" {
		return output, fmt.Errorf("本节生成中断，请稍后重试")
	}
	return output, nil
}

func (r *bidGenerationRunner) Export(ctx context.Context, task *bidgen.Task) (*bidgen.Artifact, error) {
	var snapshot bidGenerationSnapshot
	if json.Unmarshal(task.State.RequestSnapshot, &snapshot) != nil {
		return nil, fmt.Errorf("标书原始配置不可用")
	}
	ctx, _, err := r.handler.bidGenerationContext(ctx, task, snapshot.Language)
	if err != nil {
		return nil, err
	}
	if r.handler.bidGenerationStore != nil {
		current, err := r.handler.bidGenerationStore.Get(ctx, task.ID)
		if err != nil || current.Status != bidgen.StatusRunning {
			return nil, fmt.Errorf("标书任务已暂停或停止")
		}
	}
	return r.handler.exportBidGeneration(ctx, task)
}

func boolPointer(value bool) *bool { return &value }
