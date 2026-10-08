package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	stderrors "errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

const generatedDocumentPrefix = "generated-documents/"

var documentRequests singleflight.Group
var documentMessageLocks [64]sync.Mutex
var bidWritingRequest = regexp.MustCompile(`(?i)(生成|撰写|编写|起草|制作|编制|写|write|draft).*(标书|投标文件|响应文件|\bword\b|\bdocx\b)`)

type generateDocumentRequest struct {
	Format               string `json:"format"`
	IncludeContinuations *bool  `json:"include_continuations"`
}

type generatedDocumentResult struct {
	artifactListItem
	MessageID        string                        `json:"message_id"`
	SourceMessageIDs []string                      `json:"source_message_ids"`
	Truncated        bool                          `json:"truncated"`
	Formatting       *types.DocumentFormattingInfo `json:"formatting,omitempty"`
	artifact         types.MessageArtifact
	created          bool
	contextChanged   bool
}

// GenerateMessageDocument derives a Word draft from persisted answers. A caller
// can neither submit replacement bid facts nor export a different user's chat.
func (h *Handler) GenerateMessageDocument(c *gin.Context) {
	ctx := c.Request.Context()
	sessionID, messageID := paramSessionID(c), strings.TrimSpace(c.Param("message_id"))
	if sessionID == "" || messageID == "" {
		_ = c.Error(errors.NewBadRequestError("session_id and message_id are required"))
		return
	}
	var req generateDocumentRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(errors.NewBadRequestError("Invalid document request"))
		return
	}
	if req.Format != "" && req.Format != "docx" {
		_ = c.Error(errors.NewBadRequestError("Supported document format: docx"))
		return
	}
	owned, err := h.sessionService.GetOwnedSession(ctx, sessionID)
	if err != nil || owned == nil {
		_ = c.Error(errors.NewNotFoundError("Session not found"))
		return
	}
	include := req.IncludeContinuations == nil || *req.IncludeContinuations
	messageKey := fmt.Sprintf("%d/%s/%s", owned.TenantID, sessionID, messageID)
	key := fmt.Sprintf("%s/%t", messageKey, include)
	value, err, _ := documentRequests.Do(key, func() (interface{}, error) {
		// Serialize both export modes through persistence, so concurrent Word
		// requests cannot overwrite another request's newly attached file.
		digest := sha256.Sum256([]byte(messageKey))
		lock := &documentMessageLocks[int(digest[0])%len(documentMessageLocks)]
		lock.Lock()
		defer lock.Unlock()
		message, err := h.messageService.GetMessage(ctx, sessionID, messageID)
		if err != nil || message == nil {
			return nil, ErrMessageDocumentNotFound
		}
		if !documentAnswerReady(message) {
			return nil, ErrMessageDocumentNotReady
		}
		result, err := h.prepareMessageDocument(ctx, owned, message, include)
		if err != nil {
			var formatErr *tenderFormattingError
			if stderrors.As(err, &formatErr) {
				if saveErr := h.messageService.UpdateMessage(ctx, message); saveErr != nil {
					return nil, saveErr
				}
			}
			return nil, err
		}
		if result.created {
			message.Artifacts = append(message.Artifacts, result.artifact)
		}
		if result.created || result.contextChanged {
			if err := h.messageService.UpdateMessage(ctx, message); err != nil {
				if result.created {
					h.discardGeneratedDocument(ctx, message, result.artifact)
				}
				return nil, err
			}
		}
		return result, nil
	})
	if err != nil {
		var formatErr *tenderFormattingError
		if stderrors.As(err, &formatErr) {
			c.JSON(http.StatusConflict, gin.H{"success": false, "message": formatErr.Error(), "data": gin.H{"formatting": formatErr.Info}})
			return
		}
		switch {
		case stderrors.Is(err, ErrMessageDocumentNotFound):
			_ = c.Error(errors.NewNotFoundError("Document message not found"))
		case stderrors.Is(err, ErrMessageDocumentNotReady):
			_ = c.Error(errors.NewConflictError("The answer must finish before generating a document"))
		case stderrors.Is(err, ErrMessageDocumentInvalidChain):
			_ = c.Error(errors.NewBadRequestError("Invalid document continuation chain"))
		case stderrors.Is(err, ErrMessageDocumentTooLarge):
			_ = c.Error(errors.NewBadRequestError("Document content exceeds size limit"))
		default:
			logger.Errorf(ctx, "Generate Word document failed: %v", err)
			_ = c.Error(errors.NewInternalServerError("Failed to generate Word document"))
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": value})
}

// loadDocumentMessages reads through the selected answer, rather than the UI's
// paginated window or the model's shorter conversational-memory budget.
func (h *Handler) loadDocumentMessages(ctx context.Context, target *types.Message) ([]*types.Message, error) {
	const pageSize, maxMessages = 200, 4000
	var messages []*types.Message
	for page := 1; len(messages) < maxMessages; page++ {
		batch, err := h.messageService.GetMessagesBySession(ctx, target.SessionID, page, pageSize)
		if err != nil {
			return nil, err
		}
		for _, message := range batch {
			if message == nil {
				continue
			}
			if message.SessionID != target.SessionID {
				return nil, ErrMessageDocumentInvalidChain
			}
			if message.ID == target.ID {
				return append(messages, target), nil
			}
			messages = append(messages, message)
		}
		if len(batch) < pageSize {
			return nil, ErrMessageDocumentNotFound
		}
	}
	return nil, ErrMessageDocumentTooLarge
}

func (h *Handler) prepareMessageDocument(ctx context.Context, owned *types.Session, message *types.Message, include bool) (*generatedDocumentResult, error) {
	if owned == nil || message == nil || message.SessionID != owned.ID {
		return nil, ErrMessageDocumentNotFound
	}
	if h.fileService == nil {
		return nil, fmt.Errorf("document storage unavailable")
	}
	var history []*types.Message
	if include {
		var err error
		history, err = h.loadDocumentMessages(ctx, message)
		if err != nil {
			return nil, err
		}
	} else {
		history = []*types.Message{message}
	}
	document, err := assembleMessageDocument(history, message.ID, include)
	if err != nil {
		return nil, err
	}
	// A body-only export still needs its own task's source requirements. Its
	// content remains exactly the selected answer; history supplies metadata.
	if !include {
		history, err = h.loadDocumentMessages(ctx, message)
		if err != nil {
			return nil, err
		}
	}
	before, _ := message.ExecutionContext.Value()
	sourceDocument := document
	if !include && len(history) > 1 {
		if chain, chainErr := assembleMessageDocument(history, message.ID, true); chainErr == nil {
			sourceDocument.RootMessageID = chain.RootMessageID
			sourceDocument.IsBid = chain.IsBid
		}
	}
	data, _, err := h.buildTenderDocument(ctx, owned, message, history, sourceDocument)
	if err != nil {
		return nil, err
	}
	after, _ := message.ExecutionContext.Value()
	contextChanged := fmt.Sprint(before) != fmt.Sprint(after)
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	sourcePath := generatedDocumentPrefix + document.RootMessageID + "/draft.docx"
	for i, artifact := range message.Artifacts {
		if !artifact.Deleted() && artifact.SourcePath == sourcePath && artifact.ContentHash == hash {
			result := documentResult(message.ID, i, artifact, document, false)
			result.Formatting = message.DocumentFormatting
			result.contextChanged = contextChanged
			return result, nil
		}
	}
	fileName := documentFileName(document.Title)
	fileService, storageCtx, ok := h.resolveArtifactFileService(ctx, owned.TenantID, "", "", "document generation")
	if !ok {
		return nil, fmt.Errorf("document storage unavailable")
	}
	ref, err := fileService.SaveBytes(storageCtx, data, owned.TenantID, "document_"+uuid.NewString()+"_"+fileName, false)
	if err != nil {
		return nil, err
	}
	if h.resourceCatalog != nil {
		if _, ok := types.ParseResourcePath(ref); ok {
			if err := h.resourceCatalog.Bind(storageCtx, ref, types.ResourceOwnerMessage, message.ID, types.ResourceRelationArtifact); err != nil {
				_ = fileService.DeleteFile(storageCtx, ref)
				return nil, fmt.Errorf("bind document to message: %w", err)
			}
		}
	}
	now := time.Now().UTC()
	artifact := types.MessageArtifact{URL: ref, FileName: fileName, FileType: ".docx", FileSize: int64(len(data)), ContentHash: hash, SourcePath: sourcePath, ModTime: now, CreatedAt: now}
	result := documentResult(message.ID, len(message.Artifacts), artifact, document, true)
	result.Formatting = message.DocumentFormatting
	result.contextChanged = contextChanged
	return result, nil
}

func documentResult(messageID string, index int, artifact types.MessageArtifact, document messageDocument, created bool) *generatedDocumentResult {
	return &generatedDocumentResult{
		artifactListItem: artifactListItem{Index: index, Handle: artifactHandle(artifact), FileName: artifact.FileName, FileType: artifact.FileType, FileSize: artifact.FileSize, SourcePath: artifact.SourcePath, ModTime: artifact.ModTime, CreatedAt: artifact.CreatedAt},
		MessageID:        messageID, SourceMessageIDs: document.MessageIDs, Truncated: document.Truncated, artifact: artifact, created: created,
	}
}

func documentFileName(title string) string {
	var name strings.Builder
	count := 0
	for _, r := range title {
		if unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|`, r) {
			continue
		}
		name.WriteRune(r)
		count++
		if count >= 60 {
			break
		}
	}
	base := strings.Trim(name.String(), " .")
	if base == "" {
		base = "文档"
	}
	return base + "_草稿.docx"
}

func wantsAutomaticDocument(query string) bool {
	query = strings.TrimSpace(query)
	for _, word := range []string{
		"如何", "怎么", "能否", "是否", "有哪些", "需要哪些", "是什么", "什么是",
		"介绍一下", "解释一下", "检索", "搜索", "查询", "查找",
		"只要文字", "只要文本", "不要文档", "不生成文档",
	} {
		if strings.Contains(query, word) {
			return false
		}
	}
	return bidWritingRequest.MatchString(query)
}

func hasGeneratedDocument(message *types.Message) bool {
	if message == nil {
		return false
	}
	for _, artifact := range message.Artifacts {
		if !artifact.Deleted() && strings.HasPrefix(artifact.SourcePath, generatedDocumentPrefix) {
			return true
		}
	}
	return false
}

func (h *Handler) configureDocumentRequest(ctx context.Context, owned *types.Session, request *CreateKnowledgeQARequest, snapshot *types.MessageExecutionContext) error {
	snapshot.DocumentRequested = wantsAutomaticDocument(request.Query)
	if request.GenerateDocument != nil {
		snapshot.DocumentRequested = *request.GenerateDocument
	}
	parentID := strings.TrimSpace(request.ContinuationOfMessageID)
	if parentID == "" && !isDocumentContinuationPrompt(request.Query) {
		return nil
	}
	if h.messageService == nil {
		if parentID != "" {
			return errors.NewBadRequestError("Original continuation answer unavailable")
		}
		return nil
	}
	recent, err := h.messageService.GetRecentMessagesBySession(ctx, owned.ID, 1)
	if err != nil || len(recent) != 1 || !documentAnswerReady(recent[0]) ||
		(parentID != "" && recent[0].ID != parentID) {
		if parentID != "" {
			return errors.NewBadRequestError("Continuation must reference the current completed answer")
		}
		return nil
	}
	parent := recent[0]
	if parent.SessionID != owned.ID || (parentID != "" && !isDocumentContinuationPrompt(request.Query)) {
		return errors.NewBadRequestError("Invalid continuation request")
	}
	snapshot.ContinuationOfMessageID = parent.ID
	if request.GenerateDocument != nil {
		return nil
	}
	snapshot.DocumentRequested = parent.ExecutionContext.DocumentRequested || hasGeneratedDocument(parent)
	if !snapshot.DocumentRequested {
		// Older conversations did not save document intent. Recover only the
		// exact continuation chain's original user request, never a random bid
		// mention earlier in the conversation.
		history, err := h.loadDocumentMessages(ctx, parent)
		if err != nil {
			return nil // continuation itself remains available if export cannot be inferred
		}
		document, err := assembleMessageDocument(history, parent.ID, true)
		if err != nil {
			return nil
		}
		for i, message := range history {
			if message.ID == document.RootMessageID && i > 0 && history[i-1].Role == "user" {
				snapshot.DocumentRequested = wantsAutomaticDocument(history[i-1].Content)
				break
			}
		}
	}
	return nil
}

func (h *Handler) prepareAutomaticMessageDocument(ctx context.Context, message *types.Message) *types.MessageArtifact {
	if !documentAnswerReady(message) {
		return nil
	}
	if h.fileService == nil || h.sessionService == nil || h.messageService == nil {
		return nil
	}
	owned, err := h.sessionService.GetOwnedSession(ctx, message.SessionID)
	if err != nil || owned == nil {
		logger.Warnf(ctx, "Automatic Word document: session unavailable")
		return nil
	}
	result, err := h.prepareMessageDocument(ctx, owned, message, true)
	if err != nil {
		// A document failure must never destroy a successfully generated bid.
		// The completed answer still offers the explicit Word action for retry.
		logger.Warnf(ctx, "Automatic Word document failed for message %s: %v", message.ID, err)
		return nil
	}
	if result.created {
		message.Artifacts = append(message.Artifacts, result.artifact)
		return &result.artifact
	}
	return nil
}

func (h *Handler) discardGeneratedDocument(ctx context.Context, message *types.Message, artifact types.MessageArtifact) {
	tenantID, _ := types.TenantIDFromContext(ctx)
	fileService, storageCtx, ok := h.resolveArtifactFileService(ctx, tenantID, artifact.URL, "", "document cleanup")
	if !ok {
		return
	}
	if h.resourceCatalog != nil {
		if _, valid := types.ParseResourcePath(artifact.URL); valid {
			remaining, err := h.resourceCatalog.Release(storageCtx, artifact.URL, types.ResourceOwnerMessage, message.ID)
			if err != nil || remaining > 0 {
				return
			}
		}
	}
	_ = fileService.DeleteFile(storageCtx, artifact.URL)
}
