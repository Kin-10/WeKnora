package session

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type documentOwnerStub struct {
	interfaces.SessionService
	mu                    sync.Mutex
	denied                bool
	ownedCalls, readCalls int
	ownedNotice           chan struct{}
}

func (s *documentOwnerStub) GetOwnedSession(_ context.Context, id string) (*types.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ownedCalls++
	if s.ownedNotice != nil {
		s.ownedNotice <- struct{}{}
	}
	if s.denied || id != "bid-session" {
		return nil, apperrors.ErrSessionNotFound
	}
	return &types.Session{ID: id, TenantID: 42}, nil
}

func (s *documentOwnerStub) GetSession(_ context.Context, id string) (*types.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readCalls++
	// Read access intentionally differs from write ownership: tenant admins
	// may inspect channel conversations they cannot mutate.
	return &types.Session{ID: id, TenantID: 42}, nil
}

type documentMessageStub struct {
	interfaces.MessageService
	mu                     sync.Mutex
	messages               []*types.Message
	updateErr              error
	pageCalls, updateCalls int
	recentOverride         []*types.Message
	messageOverride        *types.Message
	readNotice             chan struct{}
}

func copyDocumentMessage(message *types.Message) *types.Message {
	if message == nil {
		return nil
	}
	copy := *message
	copy.Artifacts = append(types.MessageArtifacts(nil), message.Artifacts...)
	copy.AgentSteps = append(types.AgentSteps(nil), message.AgentSteps...)
	return &copy
}

func (s *documentMessageStub) GetMessage(_ context.Context, sid, id string) (*types.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readNotice != nil {
		s.readNotice <- struct{}{}
	}
	if s.messageOverride != nil {
		return copyDocumentMessage(s.messageOverride), nil
	}
	for _, message := range s.messages {
		if message.SessionID == sid && message.ID == id {
			return copyDocumentMessage(message), nil
		}
	}
	return nil, errors.New("message not found")
}

func (s *documentMessageStub) GetMessagesBySession(_ context.Context, _ string, page, size int) ([]*types.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pageCalls++
	start := (page - 1) * size
	if start >= len(s.messages) {
		return nil, nil
	}
	end := min(start+size, len(s.messages))
	out := make([]*types.Message, 0, end-start)
	for _, message := range s.messages[start:end] {
		out = append(out, copyDocumentMessage(message))
	}
	return out, nil
}

func (s *documentMessageStub) GetRecentMessagesBySession(_ context.Context, sid string, limit int) ([]*types.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recentOverride != nil {
		return s.recentOverride, nil
	}
	var out []*types.Message
	for i := len(s.messages) - 1; i >= 0 && len(out) < limit; i-- {
		if s.messages[i].SessionID == sid {
			out = append(out, copyDocumentMessage(s.messages[i]))
		}
	}
	return out, nil
}

func (s *documentMessageStub) UpdateMessage(_ context.Context, message *types.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateCalls++
	if s.updateErr != nil {
		return s.updateErr
	}
	for i, old := range s.messages {
		if old.ID == message.ID && old.SessionID == message.SessionID {
			s.messages[i] = copyDocumentMessage(message)
			return nil
		}
	}
	return errors.New("message not found")
}

type documentFileStub struct {
	interfaces.FileService
	mu               sync.Mutex
	files            map[string][]byte
	saveErr          error
	saves            int
	deleted          []string
	saveNotice       chan struct{}
	firstSaveRelease chan struct{}
}

func (s *documentFileStub) SaveBytes(ctx context.Context, data []byte, tenantID uint64, _ string, temp bool) (string, error) {
	if tenantID != 42 || types.MustTenantIDFromContext(ctx) != 42 || temp {
		return "", errors.New("wrong document storage owner")
	}
	s.mu.Lock()
	if s.saveErr != nil {
		s.mu.Unlock()
		return "", s.saveErr
	}
	s.saves++
	ref := fmt.Sprintf("local://42/exports/document-%d.docx", s.saves)
	s.files[ref] = append([]byte(nil), data...)
	first := s.saves == 1
	s.mu.Unlock()
	if s.saveNotice != nil {
		s.saveNotice <- struct{}{}
	}
	if first && s.firstSaveRelease != nil {
		<-s.firstSaveRelease
	}
	return ref, nil
}

func (s *documentFileStub) GetFile(_ context.Context, ref string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, found := s.files[ref]
	if !found {
		return nil, errors.New("file missing")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *documentFileStub) DeleteFile(_ context.Context, ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, ref)
	delete(s.files, ref)
	return nil
}

func documentFixture(messages ...*types.Message) (*Handler, *documentOwnerStub, *documentMessageStub, *documentFileStub, *gin.Engine) {
	owner := &documentOwnerStub{}
	store := &documentMessageStub{messages: messages}
	files := &documentFileStub{files: make(map[string][]byte)}
	h := &Handler{sessionService: owner, messageService: store, fileService: files}
	r := newArtifactTestRouter(h)
	r.POST("/sessions/:id/messages/:message_id/document", h.GenerateMessageDocument)
	return h, owner, store, files, r
}

func postMessageDocument(r *gin.Engine, messageID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/sessions/bid-session/messages/"+messageID+"/document", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func docxDocumentXML(t *testing.T, data []byte) string {
	t.Helper()
	parts := docxPackageParts(t, data)
	require.Contains(t, parts, "[Content_Types].xml")
	require.Contains(t, parts, "word/document.xml")
	require.Contains(t, parts, "word/styles.xml")
	return string(parts["word/document.xml"])
}

func docxPackageParts(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err, "download must be a genuine OOXML ZIP")
	parts := make(map[string][]byte)
	for _, file := range archive.File {
		reader, err := file.Open()
		require.NoError(t, err)
		parts[file.Name], err = io.ReadAll(reader)
		require.NoError(t, err)
		require.NoError(t, reader.Close())
	}
	return parts
}

func TestGenerateMessageDocumentSelectsBidStyleWithoutChangingOrdinaryWord(t *testing.T) {
	for _, tc := range []struct {
		name, request, content string
		include                bool
		wantBid                bool
	}{
		{"bid original request", "写一份标书", "# 当前项目文件\n\n原始正文", true, true},
		{"bid body only", "", "# 血站采购投标文件\n\n原始正文", false, true},
		{"bid formal chapter fragment", "", "## 投标函\n原始正文\n## 商务响应\n响应内容", false, true},
		{"ordinary Word", "生成一份 Word 工作总结", "# 工作总结\n\n原始正文", true, false},
		{"bid explanation", "标书是什么", "# 投标文件\n\n原始正文\n## 投标函\n## 商务响应", true, false},
		{"bid guide", "写一份标书编制指南", "# 标书\n\n原始正文", true, false},
		{"anonymous technical bid", "编写技术暗标投标文件", "# 技术暗标响应文件\n\n原始正文\n## 技术响应\n## 项目实施方案", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer := documentMessage("answer", "assistant", tc.content)
			var messages []*types.Message
			if tc.request != "" {
				messages = append(messages, documentMessage("write", "user", tc.request))
			}
			messages = append(messages, answer)
			_, _, store, files, r := documentFixture(messages...)
			w := postMessageDocument(r, answer.ID, fmt.Sprintf(`{"format":"docx","include_continuations":%t}`, tc.include))
			require.Equal(t, 200, w.Code, w.Body.String())
			persisted := store.messages[len(store.messages)-1]
			require.Equal(t, answer.Content, persisted.Content, "styling must not change the chat answer")
			require.Len(t, persisted.Artifacts, 1)
			parts := docxPackageParts(t, files.files[persisted.Artifacts[0].URL])
			document := string(parts["word/document.xml"])
			require.Contains(t, document, "原始正文")
			if tc.wantBid {
				require.Contains(t, parts, "word/header1.xml")
				require.Contains(t, parts, "word/footer1.xml")
				require.Contains(t, document, "<w:headerReference")
				require.Contains(t, document, "<w:footerReference")
				require.Contains(t, document, "<w:titlePg")
				require.Contains(t, string(parts["word/footer1.xml"]), "PAGE")
			} else {
				require.NotContains(t, parts, "word/header1.xml")
				require.NotContains(t, parts, "word/footer1.xml")
				require.NotContains(t, document, "<w:headerReference")
				require.NotContains(t, document, "<w:footerReference")
			}
		})
	}
}

func TestGenerateMessageDocumentOwnershipAndReadiness(t *testing.T) {
	for _, tc := range []struct {
		name               string
		denied, incomplete bool
		body               string
		status             int
	}{
		{"readable but not owned", true, false, `{"format":"docx"}`, 404},
		{"unfinished", false, true, `{"format":"docx"}`, 409},
		{"unsupported format", false, false, `{"format":"pdf"}`, 400},
		{"invalid JSON", false, false, `{"format":`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer := documentMessage("answer", "assistant", "# 采购标书\n真实正文")
			answer.IsCompleted = !tc.incomplete
			_, owner, store, files, r := documentFixture(answer)
			owner.denied = tc.denied
			w := postMessageDocument(r, "answer", tc.body)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.Zero(t, owner.readCalls, "export must use strict ownership")
			require.Zero(t, files.saves)
			require.Zero(t, store.updateCalls)
		})
	}
}

func TestGenerateMessageDocumentAuthenticatedDownloadAndIdempotency(t *testing.T) {
	answer := documentMessage("answer", "assistant", "# 血站采购标书\n\n真实正文\n\n| 项目 | 参数 |\n|---|---|\n| 设备 | 25℃ |")
	_, owner, store, files, r := documentFixture(answer)
	// Extra request properties can never replace facts in the persisted answer.
	w := postMessageDocument(r, "answer", `{"format":"docx","include_continuations":true,"content":"伪造企业资质","markdown":"伪造产品参数"}`)
	require.Equal(t, 200, w.Code, w.Body.String())
	var first struct {
		Success bool                    `json:"success"`
		Data    generatedDocumentResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &first))
	require.True(t, first.Success)
	require.Equal(t, "answer", first.Data.MessageID)
	require.Equal(t, 0, first.Data.Index)
	require.NotContains(t, w.Body.String(), "local://")
	request := httptest.NewRequest(http.MethodGet, "/sessions/bid-session/messages/answer/artifacts/0/download", nil)
	download := httptest.NewRecorder()
	r.ServeHTTP(download, request)
	require.Equal(t, 200, download.Code, download.Body.String())
	require.Equal(t, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", download.Header().Get("Content-Type"))
	require.Contains(t, download.Header().Get("Content-Disposition"), "attachment;")
	require.Contains(t, download.Header().Get("Content-Disposition"), "filename*=UTF-8''")
	xml := docxDocumentXML(t, download.Body.Bytes())
	require.Contains(t, xml, "真实正文")
	require.Contains(t, xml, "25℃")
	require.Contains(t, xml, "<w:tbl>")
	require.NotContains(t, xml, "伪造")
	second := postMessageDocument(r, "answer", `{"format":"docx","include_continuations":true}`)
	require.Equal(t, 200, second.Code, second.Body.String())
	var repeated struct {
		Data generatedDocumentResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &repeated))
	require.Equal(t, first.Data.Index, repeated.Data.Index)
	require.Equal(t, first.Data.FileName, repeated.Data.FileName)
	require.Equal(t, 1, files.saves)
	require.Equal(t, 1, store.updateCalls)
	require.Equal(t, answer.Content, store.messages[0].Content)
	require.Len(t, store.messages[0].Artifacts, 1)
	require.Equal(t, 2, owner.ownedCalls)
}

func TestGenerateMessageDocumentContinuationBoundariesAndPagination(t *testing.T) {
	messages := []*types.Message{documentMessage("older-user", "user", "写另一个项目的标书"), documentMessage("older-answer", "assistant", "不能混入的旧项目")}
	for i := 0; i < 200; i++ {
		messages = append(messages, documentMessage(fmt.Sprintf("older-%d", i), "user", "无关历史"))
	}
	messages = append(messages,
		documentMessage("write", "user", "写一份标书"),
		documentMessage("root", "assistant", "# 当前项目标书\n第一章正文"),
		documentMessage("continue", "user", "继续"),
		explicitDocumentContinuation(documentMessage("target", "assistant", "第二章正文"), "root"),
		documentMessage("later-user", "user", "新项目"), documentMessage("later-answer", "assistant", "不能混入的后续项目"),
	)
	_, _, store, files, r := documentFixture(messages...)
	w := postMessageDocument(r, "target", `{"format":"docx","include_continuations":true}`)
	require.Equal(t, 200, w.Code, w.Body.String())
	var result struct {
		Data generatedDocumentResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	require.Equal(t, []string{"root", "target"}, result.Data.SourceMessageIDs)
	require.Equal(t, 2, store.pageCalls, "export must read beyond the UI/model history window")
	xml := docxDocumentXML(t, files.files[store.messages[205].Artifacts[0].URL])
	require.Contains(t, xml, "第一章正文")
	require.Contains(t, xml, "第二章正文")
	require.NotContains(t, xml, "不能混入")
	require.NotContains(t, xml, "无关历史")
}

func TestGenerateMessageDocumentFailurePreservesPersistedAnswer(t *testing.T) {
	for _, mode := range []string{"storage", "persistence"} {
		t.Run(mode, func(t *testing.T) {
			answer := documentMessage("answer", "assistant", "# 标书\n原始正文")
			answer.Artifacts = types.MessageArtifacts{{URL: "local://42/exports/existing.txt", FileName: "existing.txt"}}
			_, _, store, files, r := documentFixture(answer)
			files.files[answer.Artifacts[0].URL] = []byte("existing")
			if mode == "storage" {
				files.saveErr = errors.New("storage offline")
			} else {
				store.updateErr = errors.New("database offline")
			}
			w := postMessageDocument(r, "answer", `{"format":"docx"}`)
			require.Equal(t, 500, w.Code, w.Body.String())
			require.Equal(t, answer.Content, store.messages[0].Content)
			require.Len(t, store.messages[0].Artifacts, 1)
			require.Equal(t, []byte("existing"), files.files[answer.Artifacts[0].URL])
			if mode == "persistence" {
				require.Equal(t, []string{"local://42/exports/document-1.docx"}, files.deleted)
			} else {
				require.Empty(t, files.deleted)
			}
		})
	}
}

func TestGenerateMessageDocumentRejectsForeignHistory(t *testing.T) {
	foreign := documentMessage("foreign", "assistant", "另一个会话的敏感资料")
	foreign.SessionID = "another-session"
	answer := explicitDocumentContinuation(documentMessage("answer", "assistant", "续写正文"), "foreign")
	_, _, store, files, r := documentFixture(foreign, documentMessage("continue", "user", "继续"), answer)
	w := postMessageDocument(r, "answer", `{"format":"docx"}`)
	require.Equal(t, 400, w.Code, w.Body.String())
	require.Zero(t, files.saves)
	require.Zero(t, store.updateCalls)
	require.NotContains(t, w.Body.String(), "敏感资料")
}

func TestGenerateMessageDocumentRejectsMessageOutsideOwnedSession(t *testing.T) {
	for _, include := range []string{"true", "false"} {
		t.Run("include="+include, func(t *testing.T) {
			foreign := documentMessage("answer", "assistant", "# 外部敏感正文")
			foreign.SessionID = "foreign-session"
			_, _, store, files, r := documentFixture()
			store.messageOverride = foreign
			w := postMessageDocument(r, "answer", `{"format":"docx","include_continuations":`+include+`}`)
			require.Equal(t, 404, w.Code, w.Body.String())
			require.Zero(t, files.saves)
			require.Zero(t, store.updateCalls)
			require.NotContains(t, w.Body.String(), "敏感正文")
		})
	}
}

func TestGenerateMessageDocumentConcurrentExportModesRetainBothArtifacts(t *testing.T) {
	root := documentMessage("root", "assistant", "# 标书\n第一章正文")
	target := explicitDocumentContinuation(documentMessage("target", "assistant", "第二章正文"), "root")
	_, owner, store, files, r := documentFixture(documentMessage("write", "user", "写一份标书"), root,
		documentMessage("continue", "user", "继续"), target)
	owner.ownedNotice = make(chan struct{}, 2)
	store.readNotice = make(chan struct{}, 2)
	files.saveNotice = make(chan struct{}, 2)
	files.firstSaveRelease = make(chan struct{})
	var release sync.Once
	unblock := func() { release.Do(func() { close(files.firstSaveRelease) }) }
	t.Cleanup(unblock)
	results := make(chan *httptest.ResponseRecorder, 2)
	go func() { results <- postMessageDocument(r, "target", `{"format":"docx","include_continuations":false}`) }()
	select {
	case <-files.saveNotice:
	case <-time.After(5 * time.Second):
		t.Fatal("first export did not reach storage")
	}
	<-owner.ownedNotice
	<-store.readNotice
	go func() { results <- postMessageDocument(r, "target", `{"format":"docx","include_continuations":true}`) }()
	select {
	case <-owner.ownedNotice:
	case <-time.After(5 * time.Second):
		t.Fatal("second export did not pass ownership")
	}
	// An unprotected second mode loads stale metadata while the first SaveBytes
	// waits. A protected mode waits for the first persist before loading it.
	select {
	case <-store.readNotice:
	case <-time.After(100 * time.Millisecond):
	}
	unblock()
	for range 2 {
		select {
		case result := <-results:
			require.Equal(t, 200, result.Code, result.Body.String())
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent export did not complete")
		}
	}
	require.Equal(t, 2, files.saves)
	require.Equal(t, 2, store.updateCalls)
	require.Len(t, store.messages[3].Artifacts, 2, "different modes must not overwrite each other's metadata")
	var single, combined int
	for _, artifact := range store.messages[3].Artifacts {
		xml := docxDocumentXML(t, files.files[artifact.URL])
		require.Contains(t, xml, "第二章正文")
		if strings.Contains(xml, "第一章正文") {
			combined++
		} else {
			single++
		}
	}
	require.Equal(t, 1, combined)
	require.Equal(t, 1, single)
	require.NotEqual(t, store.messages[3].Artifacts[0].ContentHash, store.messages[3].Artifacts[1].ContentHash)
	require.Equal(t, target.Content, store.messages[3].Content)
}

func TestConfigureDocumentRequestIntentAndContinuationOwnership(t *testing.T) {
	for _, query := range []string{"写一份标书", "生成投标文件", "标书是什么", "如何生成标书", "写一份标书，只要文字",
		"生成标书需要哪些企业资质？", "招标文件要求编写标书的格式是什么", "帮我检索生成标书的模板"} {
		t.Run(query, func(t *testing.T) {
			h, _, _, _, _ := documentFixture()
			var snapshot types.MessageExecutionContext
			require.NoError(t, h.configureDocumentRequest(context.Background(), &types.Session{ID: "bid-session"}, &CreateKnowledgeQARequest{Query: query}, &snapshot))
			require.Equal(t, query == "写一份标书" || query == "生成投标文件", snapshot.DocumentRequested)
		})
	}
	for _, mode := range []string{"inherit", "explicit text only", "stale parent", "foreign session", "new instruction"} {
		t.Run(mode, func(t *testing.T) {
			parent := documentMessage("parent", "assistant", "# 标书\n第一章")
			parent.ExecutionContext.DocumentRequested = true
			h, _, store, _, _ := documentFixture(parent)
			request := &CreateKnowledgeQARequest{Query: "继续", ContinuationOfMessageID: "parent"}
			switch mode {
			case "explicit text only":
				value := false
				request.GenerateDocument = &value
			case "stale parent":
				store.messages = append(store.messages, documentMessage("latest", "assistant", "最新回答"))
			case "foreign session":
				foreign := copyDocumentMessage(parent)
				foreign.SessionID = "foreign-session"
				store.recentOverride = []*types.Message{foreign}
			case "new instruction":
				request.Query = "写另一份标书"
			}
			var snapshot types.MessageExecutionContext
			err := h.configureDocumentRequest(context.Background(), &types.Session{ID: "bid-session"}, request, &snapshot)
			if mode == "inherit" || mode == "explicit text only" {
				require.NoError(t, err)
				require.Equal(t, "parent", snapshot.ContinuationOfMessageID)
				require.Equal(t, mode == "inherit", snapshot.DocumentRequested)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestConfigureDocumentRequestLegacyContinuationInference(t *testing.T) {
	for _, tc := range []struct {
		name, initialQuery string
		artifact, want     bool
	}{
		{"historical bid task", "写一份标书", false, true},
		{"existing generated Word", "整理项目正文", true, true},
		{"ordinary answer", "标书是什么", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := documentMessage("parent", "assistant", "# 原回答\n正文")
			if tc.artifact {
				parent.Artifacts = types.MessageArtifacts{{SourcePath: "generated-documents/parent/draft.docx"}}
			}
			h, _, _, _, _ := documentFixture(documentMessage("initial", "user", tc.initialQuery), parent)
			var snapshot types.MessageExecutionContext
			require.NoError(t, h.configureDocumentRequest(context.Background(), &types.Session{ID: "bid-session"}, &CreateKnowledgeQARequest{Query: "继续"}, &snapshot))
			require.Equal(t, "parent", snapshot.ContinuationOfMessageID)
			require.Equal(t, tc.want, snapshot.DocumentRequested)
		})
	}
}

func TestAutomaticDocumentCompletionPersistsBeforePublishingArtifact(t *testing.T) {
	answer := documentMessage("answer", "assistant", "# 标书\n完成的正文")
	answer.IsCompleted = false
	answer.ExecutionContext.DocumentRequested = true
	h, _, store, files, _ := documentFixture(documentMessage("write", "user", "写一份标书"), answer)
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(42))
	stream := &completionEventRecorder{}
	live := copyDocumentMessage(answer)
	streamHandler := NewAgentStreamHandler(ctx, "bid-session", "answer", "request", 42, time.Time{}, live, stream, nil, nil, nil, nil)
	require.NoError(t, streamHandler.handleComplete(ctx, event.Event{Data: event.AgentCompleteData{MessageID: "answer"}}))
	require.Empty(t, stream.events, "completion must wait for the metadata write")
	h.completeStreamAssistantMessage(ctx, &sseStreamContext{assistantMessage: live, streamHandler: streamHandler}, "写一份标书", "write")
	require.Len(t, store.messages[1].Artifacts, 1)
	require.Equal(t, 1, files.saves)
	require.Equal(t, answer.Content, store.messages[1].Content)
	require.Len(t, stream.events, 1)
	complete := stream.events[0]
	require.Equal(t, types.ResponseTypeComplete, complete.Type)
	metadata, ok := complete.Data["artifacts"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, metadata, 1)
	require.Equal(t, ".docx", metadata[0]["file_type"])
	raw, err := json.Marshal(complete.Data)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "local://")
	require.NotContains(t, string(raw), `"url"`)
}

func TestAutomaticDocumentCompletionPersistenceFailureDoesNotPublishOrDeleteExistingFiles(t *testing.T) {
	answer := documentMessage("answer", "assistant", "# 标书\n完成但尚未保存的正文")
	answer.IsCompleted = false
	answer.ExecutionContext.DocumentRequested = true
	answer.Artifacts = types.MessageArtifacts{{URL: "local://42/exports/existing.txt", FileName: "existing.txt"}}
	h, _, store, files, _ := documentFixture(documentMessage("write", "user", "写一份标书"), answer)
	files.files[answer.Artifacts[0].URL] = []byte("existing")
	store.updateErr = errors.New("database unavailable")
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(42))
	stream := &completionEventRecorder{}
	live := copyDocumentMessage(answer)
	streamHandler := NewAgentStreamHandler(ctx, "bid-session", "answer", "request", 42, time.Time{}, live, stream, nil, nil, nil, nil)
	require.NoError(t, streamHandler.handleComplete(ctx, event.Event{Data: event.AgentCompleteData{MessageID: "answer"}}))
	h.completeStreamAssistantMessage(ctx, &sseStreamContext{assistantMessage: live, streamHandler: streamHandler}, "写一份标书", "write")
	require.Equal(t, answer.Content, store.messages[1].Content)
	require.False(t, store.messages[1].IsCompleted)
	require.Len(t, store.messages[1].Artifacts, 1)
	require.Equal(t, []string{"local://42/exports/document-1.docx"}, files.deleted)
	require.Equal(t, []byte("existing"), files.files[answer.Artifacts[0].URL])
	require.NotEmpty(t, stream.events)
	for _, recorded := range stream.events {
		require.NotEqual(t, types.ResponseTypeComplete, recorded.Type)
	}
}
