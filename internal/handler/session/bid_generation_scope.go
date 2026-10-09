package session

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/Tencent/WeKnora/internal/bidformat"
	"github.com/Tencent/WeKnora/internal/bidgen"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
)

// bidScopeMarkers are query/reply phrases that already pin the drafting
// scope; the deterministic picker stays silent when the user named a volume
// or lot up front.
var bidScopeMarkers = regexp.MustCompile(`第\s*[0-9一二三四五六七八九十百零]+\s*包|[0-9一二三四五六七八九十百零]+\s*标段|采购包\s*[0-9一二三四五六七八九十百零]+|技术标|技术暗标|商务标|商务部分|技术部分|明标|暗标`)

type bidScopeOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type bidScopeQuestion struct {
	ID       string           `json:"id"`
	Label    string           `json:"label"`
	Type     string           `json:"type"`
	Required bool             `json:"required"`
	Options  []bidScopeOption `json:"options"`
}

type bidScopeCard struct {
	ID        string             `json:"id"`
	Title     string             `json:"title"`
	Questions []bidScopeQuestion `json:"questions"`
}

// bidScopeSelectionInput emits the scope picker deterministically from the
// parsed tender attachments — volumes and lots come from the original text
// via bidformat.DetectStructure, so the card appears the moment generation
// starts instead of one slow planning round later, and never costs an LLM
// call. Returns (nil, false) to let planning proceed untouched.
func (h *Handler) bidScopeSelectionInput(ctx context.Context, task *bidgen.Task, snapshot *bidGenerationSnapshot) (*bidgen.Output, bool) {
	if snapshot.Query == "" || len(snapshot.Attachments) == 0 {
		return nil, false
	}
	// An earlier reply that already names a volume or lot settles the scope.
	asked := snapshot.Query
	if replies := task.State.UserReplies; len(replies) > 0 {
		asked += "\n" + replies[len(replies)-1].Text
	}
	if bidScopeMarkers.MatchString(asked) {
		return nil, false
	}
	var builder strings.Builder
	for _, attachment := range snapshot.Attachments {
		if attachment.Content != "" {
			builder.WriteString(attachment.Content)
			builder.WriteString("\n")
		}
	}
	structure := bidformat.DetectStructure(builder.String())
	if !structure.HasChoice() {
		return nil, false
	}

	card := bidScopeCard{ID: "bid-scope-detected", Title: "选择本次标书的生成范围"}
	if structure.SeparateVolumes {
		card.Questions = append(card.Questions, bidScopeQuestion{
			ID: "volume", Type: "single", Required: true,
			Label:   "招标要求商务标与技术标分开编制，本次生成哪一册？（另一册完成后另起任务生成）",
			Options: volumeOptions(structure.AnonymousTechnical),
		})
	}
	if len(structure.Lots) > 1 {
		options := make([]bidScopeOption, 0, len(structure.Lots))
		for i, lot := range structure.Lots {
			label := lot.Label
			if lot.Detail != "" {
				label += "（" + lot.Detail + "）"
			}
			options = append(options, bidScopeOption{Value: fmt.Sprintf("lot_%d", i+1), Label: label})
		}
		card.Questions = append(card.Questions, bidScopeQuestion{
			ID: "lot", Type: "single", Required: true,
			Label:   "本次投标文件响应哪个包段？（一份投标文件只对应一个包段）",
			Options: options,
		})
	}
	body, err := json.Marshal(card)
	if err != nil {
		return nil, false
	}
	var prose strings.Builder
	prose.WriteString("已解析本次招标文件的结构，先确定生成范围再出目录：\n")
	if structure.SeparateVolumes {
		prose.WriteString("- 商务标与技术标分册编制")
		if structure.AnonymousTechnical {
			prose.WriteString("，技术标为暗标（不得出现供应商身份信息）")
		}
		prose.WriteString("；\n")
	}
	if len(structure.Lots) > 1 {
		prose.WriteString(fmt.Sprintf("- 共检测到 %d 个包段：%s；\n", len(structure.Lots), lotSummary(structure.Lots)))
	}
	prose.WriteString("选择后目录将只覆盖所选分册与包段。")
	content := prose.String() + "\n\n```weknora-input\n" + string(body) + "\n```"

	message := &types.Message{ID: uuid.NewString(), SessionID: task.SessionID, Role: "assistant", Content: content,
		IsCompleted: true, Channel: "web", RequestID: uuid.NewString(), ExecutionContext: snapshot.ExecutionContext}
	message.ExecutionContext.DocumentRequested = false
	if snapshot.CustomAgent != nil {
		message.AgentID, message.AgentTenantID = snapshot.CustomAgent.ID, snapshot.CustomAgent.TenantID
	}
	if _, err := h.messageService.CreateMessage(ctx, message); err != nil {
		return nil, false
	}
	return &bidgen.Output{Content: content, MessageID: message.ID}, true
}

func volumeOptions(anonymousTechnical bool) []bidScopeOption {
	technical := "仅技术标"
	if anonymousTechnical {
		technical = "仅技术标（暗标，正文不含供应商身份与报价）"
	}
	return []bidScopeOption{
		{Value: "technical", Label: technical},
		{Value: "commercial", Label: "仅商务标（报价与资格文件）"},
	}
}

func lotSummary(lots []bidformat.TenderLot) string {
	labels := make([]string, 0, len(lots))
	for _, lot := range lots {
		labels = append(labels, lot.Label)
	}
	return strings.Join(labels, "、")
}
