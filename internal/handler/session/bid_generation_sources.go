package session

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Tencent/WeKnora/internal/bidgen"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
)

// A source that needs user action pauses the same durable cursor. It never
// becomes model-generated draft text, and internal parser errors stay private.
func (h *Handler) bidGenerationSourceInput(ctx context.Context, task *bidgen.Task, snapshot *bidGenerationSnapshot) (*bidgen.Output, error) {
	missing, processing := false, false
	for _, id := range snapshot.AttachmentIDs {
		doc, err := h.temporaryDocuments.Get(ctx, task.TenantID, task.SessionID, id)
		if err != nil {
			return nil, nil // A transient repository error follows bounded retries.
		}
		if doc == nil || doc.Status == types.TemporaryDocumentStatusFailed {
			missing = true
		} else if doc.Status != types.TemporaryDocumentStatusReady {
			processing = true
		}
	}
	if !missing && !processing {
		return nil, nil
	}
	prose, title, label := "补充附件仍在解析，请等待解析完成后确认继续。已生成章节和当前进度已保存。", "等待附件解析", "附件解析完成后继续"
	if missing {
		prose = "本任务的部分对话附件已失效或解析失败，请用当前对话的附件按钮重新上传这些原始资料，再确认继续。已生成章节和当前进度已保存。"
		title, label = "补充任务原始附件", "已重新上传原始资料，继续当前任务"
	}
	card, _ := json.Marshal(map[string]interface{}{"id": "bid-source-recovery", "title": title,
		"questions": []interface{}{map[string]interface{}{"id": "ready", "label": label, "type": "single", "required": true,
			"options": []interface{}{map[string]string{"value": "ready", "label": "确认继续"}}}}})
	content := prose + "\n\n```weknora-input\n" + string(card) + "\n```"
	message := &types.Message{ID: uuid.NewString(), SessionID: task.SessionID, Role: "assistant", Content: content,
		IsCompleted: true, Channel: "web", RequestID: uuid.NewString(), ExecutionContext: snapshot.ExecutionContext}
	message.ExecutionContext.DocumentRequested = false
	if snapshot.CustomAgent != nil {
		message.AgentID, message.AgentTenantID = snapshot.CustomAgent.ID, snapshot.CustomAgent.TenantID
	}
	if _, err := h.messageService.CreateMessage(ctx, message); err != nil {
		return nil, fmt.Errorf("无法保存附件补充提示")
	}
	return &bidgen.Output{Content: content, MessageID: message.ID}, nil
}

// Fresh uploads may replace sources that are definitively gone or failed.
// Accessible originals remain in scope; an intermittent read error cannot
// silently discard evidence or bypass session ownership.
func (h *Handler) discardUnavailableBidAttachments(ctx context.Context, task *bidgen.Task, snapshot *bidGenerationSnapshot) error {
	retained := make(map[string]bool)
	ids := make([]string, 0, len(snapshot.AttachmentIDs))
	for _, id := range snapshot.AttachmentIDs {
		doc, err := h.temporaryDocuments.Get(ctx, task.TenantID, task.SessionID, id)
		if err != nil {
			return fmt.Errorf("无法核对任务原始附件，请稍后重试")
		}
		if doc != nil && doc.Status != types.TemporaryDocumentStatusFailed {
			retained[id] = true
			ids = append(ids, id)
		}
	}
	attachments := make(types.MessageAttachments, 0, len(snapshot.Attachments))
	for _, attachment := range snapshot.Attachments {
		if retained[attachment.ID] {
			attachments = append(attachments, attachment)
		}
	}
	snapshot.AttachmentIDs, snapshot.Attachments = ids, attachments
	return nil
}
