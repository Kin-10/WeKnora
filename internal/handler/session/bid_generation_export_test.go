package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/bidformat"
	"github.com/Tencent/WeKnora/internal/bidgen"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type bidExportMessageStub struct {
	*documentMessageStub
	createCalls     int
	createErr       error
	commitThenError bool
	readErr         error
}

func (s *bidExportMessageStub) GetMessage(ctx context.Context, sessionID, messageID string) (*types.Message, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	message, err := s.documentMessageStub.GetMessage(ctx, sessionID, messageID)
	if err != nil {
		return nil, gorm.ErrRecordNotFound
	}
	return message, nil
}

func (s *bidExportMessageStub) CreateMessage(_ context.Context, message *types.Message) (*types.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createCalls++
	if s.createErr != nil && !s.commitThenError {
		return nil, s.createErr
	}
	for _, old := range s.messages {
		if old.ID == message.ID {
			return nil, gorm.ErrDuplicatedKey
		}
	}
	s.messages = append(s.messages, copyDocumentMessage(message))
	if s.createErr != nil {
		return nil, s.createErr
	}
	return copyDocumentMessage(message), nil
}

func bidExportTask(t *testing.T) *bidgen.Task {
	t.Helper()
	snapshot := bidGenerationSnapshot{Query: "编写当前采购项目投标文件", SourceMessageID: "initiating-user", Language: "zh-CN",
		RequestState:     &types.SessionLastRequestState{AgentID: "bid-agent", AgentEnabled: true, ModelID: "bid-model"},
		ExecutionContext: types.MessageExecutionContext{KnowledgeBaseIDs: []string{"current-kb"}}}
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	first := bidgen.SectionSpec{ID: "qualification", Title: "资格证明", TargetWords: 800}
	second := bidgen.SectionSpec{ID: "response", Title: "商务响应", TargetWords: 800}
	return &bidgen.Task{ID: "task-current", TenantID: 42, SessionID: "bid-session", UserID: "owner", Status: bidgen.StatusRunning,
		State: bidgen.State{Phase: bidgen.PhaseExport, RequestSnapshot: raw,
			Plan:         &bidgen.Plan{Title: "当前采购项目投标文件", Sections: []bidgen.SectionSpec{first, second}},
			SectionIndex: 2,
			Sections: []bidgen.Section{
				{SectionSpec: second, Completed: true, Version: 1, Content: "## 商务响应\n\n产品参数有据可查。\n\n| 项目 | 响应 |\n| --- | --- |\n| 产品资料 | 【待补充】 |"},
				{SectionSpec: first, Completed: true, Version: 1, Content: "# 资格证明\n\n真实企业资格正文。\n\n## 证书目录\n证书原件随附件提供。"},
			}}}
}

func bidExportFixture(t *testing.T) (*Handler, *documentOwnerStub, *bidExportMessageStub, *documentFileStub, context.Context) {
	t.Helper()
	h, owner, base, files, _ := documentFixture(documentMessage("unrelated", "assistant", "其他项目的正文，绝不可混入"))
	store := &bidExportMessageStub{documentMessageStub: base}
	h.messageService = store
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(42))
	ctx = context.WithValue(ctx, types.UserIDContextKey, "owner")
	return h, owner, store, files, ctx
}

func TestBidGenerationExportCompilesAllSavedSectionsInApprovedOrder(t *testing.T) {
	h, _, store, files, ctx := bidExportFixture(t)
	task := bidExportTask(t)
	artifact, err := h.exportBidGeneration(ctx, task)
	require.NoError(t, err)
	require.NotEmpty(t, artifact.MessageID)
	require.Equal(t, 0, artifact.Index)
	require.Equal(t, "当前采购项目投标文件_草稿.docx", artifact.FileName)
	require.Equal(t, 1, store.createCalls)
	require.Zero(t, store.pageCalls, "no history ancestry can supply final document content")
	saved := store.messages[len(store.messages)-1]
	require.True(t, saved.IsCompleted)
	require.Equal(t, "bid-agent", saved.AgentID)
	require.Equal(t, "bid-model", saved.ModelID)
	require.Equal(t, "assistant", saved.Role)
	require.Contains(t, saved.Content, "完整正文 Word 草稿")
	require.Contains(t, saved.Content, "1 处待补充或待确认")
	require.Contains(t, saved.Content, "sandbox:")
	require.NotContains(t, saved.Content, "local://")
	require.False(t, saved.ExecutionContext.DocumentRequested)
	require.Empty(t, saved.ExecutionContext.ContinuationOfMessageID)
	require.Len(t, saved.Artifacts, 1)
	recorded := saved.Artifacts[0]
	require.Equal(t, "generated-documents/bid-task/task-current/complete.docx", recorded.SourcePath)
	data := files.files[recorded.URL]
	byteHash := sha256.Sum256(data)
	require.Equal(t, hex.EncodeToString(byteHash[:]), recorded.ContentHash)
	parts := docxPackageParts(t, data)
	xml := string(parts["word/document.xml"])
	require.Contains(t, xml, "真实企业资格正文")
	require.Contains(t, xml, "产品参数有据可查")
	require.Contains(t, xml, "【待补充】")
	require.Contains(t, xml, "<w:tbl>")
	require.Less(t, strings.Index(xml, "真实企业资格正文"), strings.Index(xml, "产品参数有据可查"))
	require.NotContains(t, xml, "其他项目")
	require.Contains(t, parts, "word/header1.xml")
	require.Contains(t, parts, "word/footer1.xml")
	markdown, err := compileBidGenerationDocument(task)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(markdown.Markdown, "# "))
	require.NotContains(t, markdown.Markdown, "\n# ")
	require.Contains(t, markdown.Markdown, "### 证书目录")
}

func TestBidGenerationExportRetryRecoversSameArtifactAndKeepsRevisions(t *testing.T) {
	h, _, store, files, ctx := bidExportFixture(t)
	task := bidExportTask(t)
	first, err := h.exportBidGeneration(ctx, task)
	require.NoError(t, err)
	task.Revision += 8 // Progress/retry bookkeeping must not create a new export.
	second, err := h.exportBidGeneration(ctx, task)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, files.saves)
	require.Equal(t, 1, store.createCalls)
	oldArtifact := store.messages[len(store.messages)-1].Artifacts[0]
	task.State.Sections[0].Content += "\n已依据补充的原始资料修订响应。"
	task.State.Sections[0].Version++
	revised, err := h.exportBidGeneration(ctx, task)
	require.NoError(t, err)
	require.NotEqual(t, first.MessageID, revised.MessageID)
	require.Equal(t, 2, files.saves)
	require.Equal(t, 2, store.createCalls)
	newArtifact := store.messages[len(store.messages)-1].Artifacts[0]
	require.Equal(t, oldArtifact.SourcePath, newArtifact.SourcePath)
	require.NotEqual(t, oldArtifact.ContentHash, newArtifact.ContentHash)
	require.NotEmpty(t, files.files[oldArtifact.URL], "the preceding output version remains available")
}

func TestBidGenerationExportConcurrentRetrySavesOnlyOnce(t *testing.T) {
	h, _, store, files, ctx := bidExportFixture(t)
	task := bidExportTask(t)
	var group sync.WaitGroup
	errors := make(chan error, 2)
	artifacts := make(chan *bidgen.Artifact, 2)
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			artifact, err := h.exportBidGeneration(ctx, task)
			artifacts <- artifact
			errors <- err
		}()
	}
	group.Wait()
	require.NoError(t, <-errors)
	require.NoError(t, <-errors)
	require.Equal(t, <-artifacts, <-artifacts)
	require.Equal(t, 1, files.saves)
	require.Equal(t, 1, store.createCalls)
}

func TestBidGenerationExportRejectsIncompleteAndUnsafeBodies(t *testing.T) {
	for _, mode := range []string{"unfinished", "empty", "heading only", "missing", "duplicate", "mismatched outline", "pending", "pending protocol", "truncated", "stored truncated", "paused", "mixed anonymous", "foreign scope", "database read"} {
		t.Run(mode, func(t *testing.T) {
			h, owner, store, files, ctx := bidExportFixture(t)
			task := bidExportTask(t)
			switch mode {
			case "unfinished":
				task.State.Sections[0].Completed = false
			case "empty":
				task.State.Sections[0].Content = " \n "
			case "heading only":
				task.State.Sections[0].Content = "## 商务响应"
			case "missing":
				task.State.Sections = task.State.Sections[:1]
			case "duplicate":
				task.State.Sections[1] = task.State.Sections[0]
			case "mismatched outline":
				task.State.Plan.Sections[0].Title = "变更但未重新完成的章"
			case "pending":
				task.State.PendingInput = "请选择合同方式"
			case "pending protocol":
				task.State.Sections[0].Content += "\n```weknora-input\n{" // Even an incomplete card cannot be exported.
			case "stored truncated":
				task.State.Sections[0].Truncated = true
			case "paused":
				task.Status = bidgen.StatusPaused
			case "truncated":
				task.State.Sections[0].LastMessageID = "truncated-answer"
				chunk := documentMessage("truncated-answer", "assistant", "截断正文")
				chunk.AgentSteps = types.AgentSteps{{Truncated: true}}
				store.messages = append(store.messages, chunk)
			case "mixed anonymous":
				task.State.Plan.Title = "当前采购项目技术暗标投标文件"
			case "foreign scope":
				owner.denied = true
			case "database read":
				store.readErr = errors.New("database unavailable")
			}
			_, err := h.exportBidGeneration(ctx, task)
			require.Error(t, err)
			require.Zero(t, files.saves)
			require.Zero(t, store.createCalls)
		})
	}
}

func TestBidGenerationExportPersistenceFailureAndAmbiguousCommit(t *testing.T) {
	for _, mode := range []string{"storage", "persistence", "commit response lost"} {
		t.Run(mode, func(t *testing.T) {
			h, _, store, files, ctx := bidExportFixture(t)
			task := bidExportTask(t)
			if mode == "storage" {
				files.saveErr = errors.New("storage offline")
			} else {
				store.createErr = errors.New("database connection interrupted")
				store.commitThenError = mode == "commit response lost"
			}
			result, err := h.exportBidGeneration(ctx, task)
			if mode == "commit response lost" {
				require.NoError(t, err)
				require.NotNil(t, result)
				repeated, retryErr := h.exportBidGeneration(ctx, task)
				require.NoError(t, retryErr)
				require.Equal(t, result, repeated)
				require.Equal(t, 1, files.saves)
				require.Empty(t, files.deleted)
			} else {
				require.Error(t, err)
				require.Nil(t, result)
				require.Len(t, store.messages, 1, "an export failure does not modify prior chat content")
				if mode == "persistence" {
					require.Len(t, files.deleted, 1)
				}
			}
		})
	}
}

func TestBidGenerationExportUsesFrozenTenderRequirementsAfterUploadsExpire(t *testing.T) {
	h, _, store, files, ctx := bidExportFixture(t)
	task := bidExportTask(t)
	task.State.Plan.Title = "当前项目技术暗标投标文件"
	task.State.Plan.Sections = []bidgen.SectionSpec{{ID: "technical", Title: "技术响应"}}
	task.State.Sections = []bidgen.Section{{SectionSpec: task.State.Plan.Sections[0], Completed: true, Content: "## 技术响应\n\n完整技术正文。"}}
	var snapshot bidGenerationSnapshot
	require.NoError(t, json.Unmarshal(task.State.RequestSnapshot, &snapshot))
	snapshot.Query = "编写技术暗标投标文件"
	snapshot.Attachments = types.MessageAttachments{tenderAttachment("expired-current")}
	snapshot.ExecutionContext.TenderFormatting = &types.TenderFormattingSnapshot{Version: 1, SourceFiles: []string{"当前招标文件.pdf"},
		Result: bidformat.Parse([]bidformat.Source{{ID: "expired-current", Name: "当前招标文件.pdf", Text: handanTenderText(t)}})}
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	task.State.RequestSnapshot = raw
	temps := &tenderTemporaryStub{docs: map[string]*types.TemporaryDocument{}}
	h.temporaryDocuments = temps
	result, err := h.exportBidGeneration(ctx, task)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Empty(t, temps.calls, "successful captured requirements do not reread expired sources")
	saved := store.messages[len(store.messages)-1]
	require.Equal(t, "tender", saved.DocumentFormatting.Mode)
	require.Equal(t, "technical", saved.DocumentFormatting.Scope)
	parts := docxPackageParts(t, files.files[saved.Artifacts[0].URL])
	require.Contains(t, string(parts["word/styles.xml"]), `w:line="600"`)
	require.Contains(t, string(parts["word/styles.xml"]), `w:sz w:val="28"`)
	require.NotContains(t, parts, "word/header1.xml")
	require.NotContains(t, parts, "word/footer1.xml")
}
