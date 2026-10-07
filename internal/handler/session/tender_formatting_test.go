package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/bidformat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type tenderTemporaryStub struct {
	interfaces.TemporaryDocumentService
	docs  map[string]*types.TemporaryDocument
	calls []string
}

func (s *tenderTemporaryStub) Get(_ context.Context, tenant uint64, session, id string) (*types.TemporaryDocument, error) {
	s.calls = append(s.calls, id)
	if tenant != 42 || session != "bid-session" {
		return nil, errors.New("wrong scope")
	}
	if doc := s.docs[id]; doc != nil {
		return doc, nil
	}
	return nil, errors.New("source expired")
}
func tenderAttachment(id string) types.MessageAttachment {
	return types.MessageAttachment{ID: id, FileName: "当前招标文件.pdf", ContentMode: "selected_chunks", Content: "所选片段没有排版条款"}
}
func handanTenderText(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../bidformat/testdata/handan-format.txt")
	require.NoError(t, err)
	return string(data)
}
func tenderReady(id, text string) *types.TemporaryDocument {
	return &types.TemporaryDocument{ID: id, TenantID: 42, SessionID: "parent-session", FileName: "当前招标文件.pdf", FileType: ".pdf", Status: types.TemporaryDocumentStatusReady, Content: text}
}

func TestTenderExportUsesFullScopedOriginalAndFrozenEvidence(t *testing.T) {
	user := documentMessage("write", "user", "编写技术暗标投标文件")
	user.Attachments = types.MessageAttachments{tenderAttachment("current")}
	answer := documentMessage("answer", "assistant", "# 技术暗标投标文件\n\n## 技术响应\n原始技术正文。\n\n| 项目 | 响应 |\n| --- | --- |\n| 检测能力 | 【待补充】 |")
	h, _, store, files, r := documentFixture(user, answer)
	temps := &tenderTemporaryStub{docs: map[string]*types.TemporaryDocument{"current": tenderReady("current", handanTenderText(t))}}
	h.temporaryDocuments = temps
	response := postMessageDocument(r, "answer", `{"format":"docx"}`)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Equal(t, []string{"current"}, temps.calls)
	persisted := store.messages[1]
	require.Equal(t, answer.Content, persisted.Content)
	require.Equal(t, "tender", persisted.DocumentFormatting.Mode)
	require.Equal(t, "technical", persisted.DocumentFormatting.Scope)
	spec, _ := persisted.ExecutionContext.TenderFormatting.Result.Resolve(bidformat.ScopeTechnical)
	require.Equal(t, 28, *spec.Body.FontSizeHalfPoints)
	require.Nil(t, spec.Table.FontSizeHalfPoints)
	parts := docxPackageParts(t, files.files[persisted.Artifacts[0].URL])
	require.NotContains(t, parts, "word/header1.xml")
	require.NotContains(t, parts, "word/footer1.xml")
	require.Contains(t, string(parts["word/styles.xml"]), `w:line="600"`)
	require.Contains(t, string(parts["word/styles.xml"]), `w:sz w:val="28"`)
	// The source service grants a copied/forked reference despite its parent
	// session row. Later expiry must not erase already captured evidence.
	delete(temps.docs, "current")
	response = postMessageDocument(r, "answer", `{"format":"docx"}`)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Len(t, temps.calls, 1)
	require.Equal(t, 1, files.saves)
}

func TestTenderSourcesBelongOnlyToOriginalWritingTask(t *testing.T) {
	oldUser := documentMessage("old-user", "user", "编写技术暗标投标文件")
	oldUser.Attachments = types.MessageAttachments{tenderAttachment("other")}
	oldAnswer := documentMessage("old-answer", "assistant", "# 上个项目投标文件\n旧正文")
	user := documentMessage("write", "user", "编写技术暗标投标文件")
	user.Attachments = types.MessageAttachments{tenderAttachment("current"), {ID: "history", FileName: "核酸采购项目投标文件.docx"}}
	answer := documentMessage("answer", "assistant", "# 技术暗标投标文件\n\n原始正文")
	h, _, store, _, r := documentFixture(oldUser, oldAnswer, user, answer)
	temps := &tenderTemporaryStub{docs: map[string]*types.TemporaryDocument{"current": tenderReady("current", handanTenderText(t)), "history": {ID: "history", TenantID: 42, Status: "ready", FileName: "核酸采购项目投标文件.docx", Content: "正文字体为黑体，二号。"}}}
	h.temporaryDocuments = temps
	response := postMessageDocument(r, "answer", `{"format":"docx"}`)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.NotContains(t, temps.calls, "other")
	require.Equal(t, []string{"当前招标文件.pdf"}, store.messages[3].DocumentFormatting.SourceFiles)
	spec, _ := store.messages[3].ExecutionContext.TenderFormatting.Result.Resolve(bidformat.ScopeTechnical)
	require.Equal(t, "宋体", *spec.Body.FontFamily)
}

func TestTenderStyleDoesNotApplyAnonymousRulesToBusiness(t *testing.T) {
	user := documentMessage("write", "user", "写一份标书")
	user.Attachments = types.MessageAttachments{tenderAttachment("current")}
	answer := documentMessage("answer", "assistant", "# 投标文件\n\n## 投标函\n原始正文")
	h, _, store, files, r := documentFixture(user, answer)
	h.temporaryDocuments = &tenderTemporaryStub{docs: map[string]*types.TemporaryDocument{"current": tenderReady("current", handanTenderText(t))}}
	response := postMessageDocument(r, "answer", `{"format":"docx"}`)
	require.Equal(t, 200, response.Code, response.Body.String())
	info := store.messages[1].DocumentFormatting
	require.Equal(t, "default", info.Mode)
	require.Equal(t, "business", info.Scope)
	parts := docxPackageParts(t, files.files[store.messages[1].Artifacts[0].URL])
	require.Contains(t, parts, "word/header1.xml")
}

func TestTenderBlocksMixedVolumesAndUnavailableFullSource(t *testing.T) {
	for _, tc := range []struct {
		name, body, status string
		ocr                bool
	}{
		{"mixed", "# 投标文件\n## 投标函\n原始商务正文\n## 技术方案\n原始技术正文", "ready", false},
		{"pending", "# 技术暗标投标文件\n原始正文", "processing", false},
		{"partial scanned OCR", "# 技术暗标投标文件\n原始正文", "ready", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := documentMessage("write", "user", "写一份标书")
			user.Attachments = types.MessageAttachments{tenderAttachment("current")}
			answer := documentMessage("answer", "assistant", tc.body)
			h, _, store, files, r := documentFixture(user, answer)
			doc := tenderReady("current", handanTenderText(t))
			doc.Status = tc.status
			if tc.ocr {
				doc.Metadata = types.JSON(`{"image_understanding":"vlm"}`)
			}
			h.temporaryDocuments = &tenderTemporaryStub{docs: map[string]*types.TemporaryDocument{"current": doc}}
			response := postMessageDocument(r, "answer", `{"format":"docx"}`)
			require.Equal(t, 409, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), `"formatting"`)
			require.Equal(t, "blocked", store.messages[1].DocumentFormatting.Mode)
			require.Equal(t, tc.body, store.messages[1].Content)
			require.Empty(t, files.files)
		})
	}
}

func TestTenderContextPersistenceFailureNeverDeletesExistingArtifact(t *testing.T) {
	user := documentMessage("write", "user", "写一份标书")
	answer := documentMessage("answer", "assistant", "# 投标文件\n\n原始正文")
	_, _, store, files, r := documentFixture(user, answer)
	require.Equal(t, 200, postMessageDocument(r, "answer", `{"format":"docx"}`).Code)
	artifact := store.messages[1].Artifacts[0]
	store.messages[1].ExecutionContext.TenderFormatting = nil
	store.messages[1].DocumentFormatting = nil
	store.updateErr = errors.New("DB write failed")
	response := postMessageDocument(r, "answer", `{"format":"docx"}`)
	require.Equal(t, 500, response.Code, response.Body.String())
	require.Contains(t, files.files, artifact.URL)
	require.Empty(t, files.deleted)
	require.Equal(t, 1, files.saves)
}

func TestBodyOnlyContinuationRetainsRootTenderRequirements(t *testing.T) {
	user := documentMessage("write", "user", "编写技术暗标投标文件")
	user.Attachments = types.MessageAttachments{tenderAttachment("current")}
	first := documentMessage("first", "assistant", "# 技术暗标投标文件\n\n第一段正文")
	continued := documentMessage("continue", "user", "继续")
	answer := documentMessage("answer", "assistant", "第二段正文")
	answer.ExecutionContext.ContinuationOfMessageID = "first"
	h, _, store, files, r := documentFixture(user, first, continued, answer)
	h.temporaryDocuments = &tenderTemporaryStub{docs: map[string]*types.TemporaryDocument{"current": tenderReady("current", handanTenderText(t))}}
	response := postMessageDocument(r, "answer", `{"format":"docx","include_continuations":false}`)
	require.Equal(t, 200, response.Code, response.Body.String())
	parts := docxPackageParts(t, files.files[store.messages[3].Artifacts[0].URL])
	require.Contains(t, string(parts["word/document.xml"]), "第二段正文")
	require.NotContains(t, string(parts["word/document.xml"]), "第一段正文")
	require.Equal(t, "tender", store.messages[3].DocumentFormatting.Mode)
}

func TestTenderWritingGuardUsesQuotedContextAndFailedAttachmentMetadata(t *testing.T) {
	h := &Handler{temporaryDocuments: &tenderTemporaryStub{docs: map[string]*types.TemporaryDocument{"current": {ID: "current", TenantID: 42, Status: "failed"}}}}
	rc := &qaRequestContext{query: "编写技术暗标投标文件", userInput: "编写技术暗标投标文件", session: &types.Session{ID: "bid-session", TenantID: 42}, assistantMessage: documentMessage("answer", "assistant", ""), attachmentMetas: types.MessageAttachments{tenderAttachment("current")}}
	h.prepareTenderWritingContext(context.Background(), rc)
	req := rc.buildQARequest()
	require.Equal(t, rc.query, req.Query)
	require.Contains(t, req.QuotedContext, "历史标书仅供措辞和结构参考")
	require.Contains(t, req.QuotedContext, "完整解析内容")
	require.Equal(t, "blocked", rc.assistantMessage.DocumentFormatting.Mode)
	ordinary := &qaRequestContext{query: "介绍系统", userInput: "介绍系统", session: rc.session, assistantMessage: documentMessage("new", "assistant", "")}
	h.prepareTenderWritingContext(context.Background(), ordinary)
	require.Empty(t, ordinary.buildQARequest().QuotedContext)
	require.Nil(t, ordinary.assistantMessage.ExecutionContext.TenderFormatting)
}

type tenderKnowledgeStub struct {
	interfaces.KnowledgeService
	rows  []*types.Knowledge
	calls [][]string
}

func (s *tenderKnowledgeStub) GetKnowledgeBatchWithSharedAccess(_ context.Context, tenant uint64, ids []string) ([]*types.Knowledge, error) {
	s.calls = append(s.calls, ids)
	if tenant != 42 {
		return nil, errors.New("wrong owner")
	}
	return s.rows, nil
}

type tenderChunksStub struct {
	interfaces.ChunkRepository
	chunks  []*types.Chunk
	tenants []uint64
	ids     []string
}

func (s *tenderChunksStub) ListChunksByKnowledgeID(_ context.Context, tenant uint64, id string) ([]*types.Chunk, error) {
	s.tenants = append(s.tenants, tenant)
	s.ids = append(s.ids, id)
	return s.chunks, nil
}

type tenderChunkService struct {
	interfaces.ChunkService
	repo interfaces.ChunkRepository
}

func (s *tenderChunkService) GetRepository() interfaces.ChunkRepository { return s.repo }

func TestTenderKnowledgeReadsOnlyExplicitAuthorizedEnabledText(t *testing.T) {
	knowledge := &tenderKnowledgeStub{rows: []*types.Knowledge{{ID: "current", TenantID: 90, FileName: "附件1.pdf", ParseStatus: "completed"}, {ID: "irrelevant", TenantID: 99, FileName: "其他招标文件.pdf", ParseStatus: "completed"}}}
	chunks := &tenderChunksStub{chunks: []*types.Chunk{
		{KnowledgeID: "current", TenantID: 90, ChunkType: types.ChunkTypeText, IsEnabled: true, Content: "招标文件\n投标文件格式要求：正文字体为宋体，四号。"},
		{KnowledgeID: "current", TenantID: 90, ChunkType: types.ChunkTypeText, IsEnabled: false, Content: "正文字体为黑体，二号。"},
		{KnowledgeID: "current", TenantID: 90, ChunkType: types.ChunkTypeSummary, IsEnabled: true, Content: "正文字体为黑体，二号。"},
		{KnowledgeID: "different", TenantID: 90, ChunkType: types.ChunkTypeText, IsEnabled: true, Content: "正文字体为黑体，二号。"},
	}}
	h := &Handler{knowledgeService: knowledge, chunkService: &tenderChunkService{repo: chunks}}
	snapshot := h.collectTenderFormatting(context.Background(), &types.Session{ID: "bid-session", TenantID: 42}, nil, []string{"current"}, nil)
	spec, info := resolveTenderInfo(snapshot, bidformat.ScopeBusiness, false)
	require.Equal(t, "tender", info.Mode)
	require.Equal(t, 28, *spec.Body.FontSizeHalfPoints)
	require.Equal(t, []uint64{90}, chunks.tenants)
	require.Equal(t, []string{"current"}, chunks.ids)
	snapshot = h.collectTenderFormatting(context.Background(), &types.Session{ID: "bid-session", TenantID: 42}, nil, []string{"denied"}, nil)
	require.NotEmpty(t, snapshot.SourceError)
	require.Len(t, chunks.ids, 1)
	data, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NotContains(t, string(data), "resource://")
	require.False(t, strings.Contains(string(data), "API key"))
}
