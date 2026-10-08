package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/bidgen"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var bidGenerationExportLocks [64]sync.Mutex

var bidGenerationPlaceholderRE = regexp.MustCompile(`【(?:待补充|待确认|待提供)[^】]*】`)

// compileBidGenerationDocument uses the durable section store, never chat
// history, model memory or the most recently visible answer, as its body.
func compileBidGenerationDocument(task *bidgen.Task) (messageDocument, error) {
	if task == nil || task.ID == "" || task.SessionID == "" || task.TenantID == 0 || task.UserID == "" {
		return messageDocument{}, fmt.Errorf("invalid bid generation export scope")
	}
	state := task.State
	if (task.Status != bidgen.StatusRunning && task.Status != bidgen.StatusCompleted) ||
		state.Phase != bidgen.PhaseExport || state.PendingMessageID != "" || state.PendingInput != "" ||
		state.Plan == nil || len(state.Plan.Sections) == 0 || len(state.Sections) != len(state.Plan.Sections) {
		return messageDocument{}, fmt.Errorf("bid generation sections are not ready for export")
	}
	title := strings.TrimSpace(state.Plan.Title)
	if title == "" || strings.ContainsAny(title, "\r\n") || !utf8.ValidString(title) || len(title) > 1000 {
		return messageDocument{}, fmt.Errorf("invalid bid generation document title")
	}
	byID := make(map[string]bidgen.Section, len(state.Sections))
	for _, section := range state.Sections {
		if section.ID == "" || !section.Completed || section.Truncated || strings.TrimSpace(section.Content) == "" ||
			!utf8.ValidString(section.Content) || types.HasUserInputRequest(section.Content) {
			return messageDocument{}, fmt.Errorf("bid generation section is incomplete or awaiting information")
		}
		if _, exists := byID[section.ID]; exists {
			return messageDocument{}, fmt.Errorf("duplicate bid generation section")
		}
		byID[section.ID] = section
	}
	var markdown strings.Builder
	fmt.Fprintf(&markdown, "# %s\n", title)
	seen := make(map[string]bool, len(state.Plan.Sections))
	for _, outline := range state.Plan.Sections {
		section, exists := byID[outline.ID]
		if !exists || seen[outline.ID] || strings.TrimSpace(outline.Title) == "" ||
			strings.ContainsAny(outline.Title, "\r\n") || outline.Title != section.Title {
			return messageDocument{}, fmt.Errorf("saved bid sections do not match the approved outline")
		}
		seen[outline.ID] = true
		body := bidGenerationSectionMarkdown(section)
		if strings.TrimSpace(body) == "" {
			return messageDocument{}, fmt.Errorf("bid generation section has no body")
		}
		if markdown.Len()+len(body)+len(outline.Title)+8 > maxMessageDocumentBytes {
			return messageDocument{}, ErrMessageDocumentTooLarge
		}
		fmt.Fprintf(&markdown, "\n## %s\n\n%s\n", outline.Title, body)
	}
	document := messageDocument{Title: title, Markdown: markdown.String(), IsBid: true}
	_, mixed := tenderDocumentScope(document, "")
	if mixed && (isAnonymousBidDocument(title) || documentHasAnonymousBidHeading(document.Markdown)) {
		return messageDocument{}, fmt.Errorf("商务标与技术暗标需要分别生成，不能合并身份或报价章节")
	}
	// The default bid cover includes bidder identities. Explicit anonymous
	// submissions must use the existing tender-specific renderer instead.
	if isAnonymousBidDocument(title) || documentHasAnonymousBidHeading(document.Markdown) {
		document.IsBid = false
	}
	return document, nil
}

// Make the approved outline the chapter heading. Model headings remain inside
// that chapter; fences and their contents are retained byte-for-byte.
func bidGenerationSectionMarkdown(section bidgen.Section) string {
	body := strings.TrimSpace(documentMarkdown(section.Content))
	lines := strings.Split(body, "\n")
	delta := 0
	if len(lines) > 0 {
		if match := bidDocumentHeadingRE.FindStringSubmatch(lines[0]); len(match) > 1 && strings.TrimSpace(match[1]) == strings.TrimSpace(section.Title) {
			first := strings.TrimLeft(lines[0], " \t")
			level := len(first) - len(strings.TrimLeft(first, "#"))
			delta = max(0, 2-level)
			lines = lines[1:]
		}
	}
	var fence string
	for i, line := range lines {
		plain := strings.TrimLeft(line, " ")
		if strings.HasPrefix(plain, "```") || strings.HasPrefix(plain, "~~~") {
			char := plain[:1]
			length := len(plain) - len(strings.TrimLeft(plain, char))
			if fence == "" {
				fence = strings.Repeat(char, length)
			} else if strings.HasPrefix(plain, fence) && strings.TrimSpace(plain[length:]) == "" {
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		if match := bidDocumentHeadingRE.FindStringSubmatch(line); len(match) > 1 {
			level := len(plain) - len(strings.TrimLeft(plain, "#"))
			lines[i] = strings.Repeat("#", min(6, max(3, level+delta))) + " " + match[1]
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// exportBidGeneration publishes one completed assistant message and artifact
// for one immutable body/scope fingerprint. A retry after the task row write
// fails recovers the same message before rendering or saving any bytes again.
func (h *Handler) exportBidGeneration(ctx context.Context, task *bidgen.Task) (*bidgen.Artifact, error) {
	document, err := compileBidGenerationDocument(task)
	if err != nil {
		return nil, err
	}
	if h.sessionService == nil || h.messageService == nil || h.fileService == nil {
		return nil, fmt.Errorf("bid generation document services unavailable")
	}
	var snapshot bidGenerationSnapshot
	if err := json.Unmarshal(task.State.RequestSnapshot, &snapshot); err != nil || snapshot.SourceMessageID == "" {
		return nil, fmt.Errorf("bid generation source snapshot unavailable")
	}
	_, mixed := tenderDocumentScope(document, snapshot.Query)
	anonymous := isAnonymousBidDocument(snapshot.Query) || isAnonymousBidDocument(document.Title) || documentHasAnonymousBidHeading(document.Markdown)
	if tender := snapshot.ExecutionContext.TenderFormatting; tender != nil {
		spec, _ := tender.Result.Resolve("technical")
		anonymous = anonymous || (spec.Anonymous != nil && *spec.Anonymous)
		mixed = mixed || tenderRequiresSeparateVolumes(tender, document.Markdown)
	}
	if anonymous && mixed {
		return nil, fmt.Errorf("商务标与技术暗标需要分别生成，不能合并身份或报价章节")
	}
	owned, err := h.sessionService.GetOwnedSession(ctx, task.SessionID)
	if err != nil || owned == nil || owned.ID != task.SessionID || owned.TenantID != task.TenantID ||
		(owned.UserID != "" && owned.UserID != task.UserID) {
		return nil, ErrMessageDocumentNotFound
	}
	// The snapshot, not current chat preferences or retrieved references, fixes
	// the inputs. Revision includes progress and retries, so it is not identity.
	frozen, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	fingerprint := sha256.Sum256(append(append([]byte(document.Markdown), 0), frozen...))
	identity := fmt.Sprintf("bid-generation/%d/%s/%s/%x", task.TenantID, task.SessionID, task.ID, fingerprint)
	messageID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(identity)).String()
	sourcePath := generatedDocumentPrefix + "bid-task/" + task.ID + "/complete.docx"
	lock := &bidGenerationExportLocks[int(fingerprint[0])%len(bidGenerationExportLocks)]
	lock.Lock()
	defer lock.Unlock()
	if existing, err := h.messageService.GetMessage(ctx, task.SessionID, messageID); err == nil && existing != nil {
		return bidGenerationSavedArtifact(existing, task.SessionID, sourcePath)
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("read saved bid generation export: %w", err)
	}
	// Completed is the core's nontruncated section sentinel. When a persisted
	// last chunk is available, also reject a contradictory truncation marker.
	for _, section := range task.State.Sections {
		if section.LastMessageID == "" {
			continue
		}
		message, err := h.messageService.GetMessage(ctx, task.SessionID, section.LastMessageID)
		if err != nil || message == nil || message.SessionID != task.SessionID || !documentAnswerReady(message) {
			return nil, fmt.Errorf("bid section completion message unavailable")
		}
		for _, step := range message.AgentSteps {
			if step.Truncated {
				return nil, fmt.Errorf("bid section was truncated and must finish before export")
			}
		}
	}
	execution := snapshot.ExecutionContext
	execution.ContinuationOfMessageID = ""
	execution.DocumentRequested = false
	execution.TenderFormatting = cloneTenderSnapshot(execution.TenderFormatting)
	message := &types.Message{ID: messageID, SessionID: task.SessionID, RequestID: task.ID,
		Role: "assistant", IsCompleted: true, ExecutionContext: execution, Channel: "web"}
	if snapshot.RequestState != nil {
		message.AgentID = snapshot.RequestState.AgentID
		message.ModelID = snapshot.RequestState.ModelID
		message.AgentTenantID = snapshot.RequestState.AgentSourceTenantID
	}
	if snapshot.CustomAgent != nil {
		message.AgentID = snapshot.CustomAgent.ID
		if message.ModelID == "" {
			message.ModelID = snapshot.CustomAgent.Config.ModelID
		}
		if message.AgentTenantID == 0 {
			message.AgentTenantID = snapshot.CustomAgent.TenantID
		}
	}
	if message.AgentTenantID == 0 {
		message.AgentTenantID = owned.TenantID
	}
	// Use an exact two-message source pair made from the initiating frozen
	// scope. buildTenderDocument can reread those files after a failed parse,
	// but cannot accidentally select a different project from conversation.
	user := &types.Message{ID: snapshot.SourceMessageID, SessionID: owned.ID, Role: "user",
		Content: snapshot.Query, Attachments: snapshot.Attachments, IsCompleted: true}
	document.RootMessageID = messageID
	document.MessageIDs = []string{messageID}
	data, _, err := h.buildTenderDocument(ctx, owned, message, []*types.Message{user, message}, document)
	if err != nil {
		return nil, err
	}
	files, storageCtx, ok := h.resolveArtifactFileService(ctx, owned.TenantID, "", "", "bid generation document")
	if !ok {
		return nil, fmt.Errorf("bid generation document storage unavailable")
	}
	fileName := documentFileName(document.Title)
	ref, err := files.SaveBytes(storageCtx, data, owned.TenantID, "document_"+messageID+"_"+fileName, false)
	if err != nil {
		return nil, err
	}
	if h.resourceCatalog != nil {
		if _, valid := types.ParseResourcePath(ref); valid {
			if err := h.resourceCatalog.Bind(storageCtx, ref, types.ResourceOwnerMessage, messageID, types.ResourceRelationArtifact); err != nil {
				_ = files.DeleteFile(storageCtx, ref)
				return nil, fmt.Errorf("bind bid generation document: %w", err)
			}
		}
	}
	byteHash := sha256.Sum256(data)
	now := time.Now().UTC()
	artifact := types.MessageArtifact{URL: ref, FileName: fileName, FileType: ".docx", FileSize: int64(len(data)),
		ContentHash: hex.EncodeToString(byteHash[:]), SourcePath: sourcePath, ModTime: now, CreatedAt: now}
	message.Artifacts = types.MessageArtifacts{artifact}
	message.CreatedAt, message.UpdatedAt = now, now
	message.Content = bidGenerationExportSummary(snapshot.Language, document, artifact, message.DocumentFormatting)
	created, createErr := h.messageService.CreateMessage(ctx, message)
	if createErr != nil {
		// A commit may have succeeded even when the caller saw a connection
		// error. Recover first, and never delete bytes referenced by a commit.
		persisted, readErr := h.messageService.GetMessage(ctx, task.SessionID, messageID)
		if readErr == nil && persisted != nil {
			result, savedErr := bidGenerationSavedArtifact(persisted, task.SessionID, sourcePath)
			if savedErr == nil {
				if persisted.Artifacts[result.Index].URL != artifact.URL {
					h.discardGeneratedDocument(ctx, message, artifact)
				}
				return result, nil
			}
		}
		if errors.Is(readErr, gorm.ErrRecordNotFound) {
			h.discardGeneratedDocument(ctx, message, artifact)
		} else {
			logger.Warnf(ctx, "Bid export persistence uncertain for %s; document blob retained", messageID)
		}
		return nil, createErr
	}
	if created == nil {
		return nil, fmt.Errorf("bid generation document message was not persisted")
	}
	return bidGenerationSavedArtifact(created, task.SessionID, sourcePath)
}

func bidGenerationSavedArtifact(message *types.Message, sessionID, sourcePath string) (*bidgen.Artifact, error) {
	if message == nil || message.SessionID != sessionID || !message.IsCompleted || message.Role != "assistant" || message.DeletedAt.Valid {
		return nil, fmt.Errorf("saved bid generation document is unavailable")
	}
	for index, artifact := range message.Artifacts {
		if artifact.SourcePath == sourcePath && !artifact.Deleted() && artifact.URL != "" && artifact.ContentHash != "" && artifact.FileType == ".docx" {
			return &bidgen.Artifact{MessageID: message.ID, Index: index, FileName: artifact.FileName, Handle: artifactHandle(artifact)}, nil
		}
	}
	return nil, fmt.Errorf("saved bid generation document artifact is unavailable")
}

func bidGenerationExportSummary(language string, document messageDocument, artifact types.MessageArtifact, formatting *types.DocumentFormattingInfo) string {
	count := len(bidGenerationPlaceholderRE.FindAllString(document.Markdown, -1))
	intro, link, notice := "已按确认的目录合并全部章节，生成完整正文 Word 草稿。", "下载 Word 草稿", "提交前请核对事实、报价、附件与招标要求。"
	if count > 0 {
		notice = fmt.Sprintf("正文保留了 %d 处待补充或待确认的原始资料标记；请补齐并核对后再提交。", count)
	}
	switch language {
	case "en", "en-US", "en-GB":
		intro, link, notice = "All chapters have been compiled in the approved outline order into a complete Word draft.", "Download Word draft", "Verify facts, prices, annexes and tender requirements before submission."
		if count > 0 {
			notice = fmt.Sprintf("The draft retains %d markers for missing or unconfirmed source material. Complete and verify them before submission.", count)
		}
	case "ja", "ja-JP":
		intro, link, notice = "確認済みの目次順に全章をまとめ、本文全体の Word 草稿を作成しました。", "Word 草稿をダウンロード", "提出前に事実、価格、添付資料、招標要件を確認してください。"
		if count > 0 {
			notice = fmt.Sprintf("草稿には補足または確認が必要な原資料の表示が %d か所残っています。提出前に補完して確認してください。", count)
		}
	case "ko", "ko-KR":
		intro, link, notice = "확인된 목차 순서대로 모든 장을 합쳐 전체 본문의 Word 초안을 생성했습니다.", "Word 초안 다운로드", "제출 전에 사실, 가격, 첨부 자료 및 입찰 요구사항을 확인하세요."
		if count > 0 {
			notice = fmt.Sprintf("원본 자료의 보완 또는 확인이 필요한 표시가 %d곳 남아 있습니다. 제출 전에 보완하고 확인하세요.", count)
		}
	case "ru", "ru-RU":
		intro, link, notice = "Все главы объединены в порядке согласованного плана в полный черновик Word.", "Скачать черновик Word", "Перед подачей проверьте факты, цены, приложения и требования закупки."
		if count > 0 {
			notice = fmt.Sprintf("В черновике осталось %d отметок о недостающих или неподтверждённых исходных материалах. Дополните и проверьте их перед подачей.", count)
		}
	}
	reference := artifactReference(artifact)
	if strings.HasPrefix(reference, "sandbox:") {
		reference = "sandbox:" + url.PathEscape(artifact.FileName)
	}
	result := intro + "\n\n[" + link + "](" + reference + ")\n\n" + notice
	if formatting != nil && formatting.Warning != "" {
		result += "\n\n" + formatting.Warning
	}
	return result
}
