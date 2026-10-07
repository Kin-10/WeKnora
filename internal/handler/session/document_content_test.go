package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func documentMessage(id, role, content string) *types.Message {
	return &types.Message{
		ID: id, SessionID: "bid-session", Role: role, Content: content,
		IsCompleted: true,
	}
}

func explicitDocumentContinuation(message *types.Message, parentID string) *types.Message {
	message.ExecutionContext.ContinuationOfMessageID = parentID
	return message
}

func TestAssembleMessageDocumentLegacyBidContinuation(t *testing.T) {
	// This is the original user's pre-feature continuation pattern. Keep the
	// root's unfinished sentence and the continuation's table exactly intact.
	first := "# 投标文件\n\n第四 法定代表人授权委托书\n致：邯郸市中心血站（采购人"
	second := "）：\n\n| 项目名称 | 采购单位 | 签订日期 | 合同金额 |\n|---|---|---|---|\n| 待补充 | 待补充 | 待补充 | 待补充 |"
	messages := []*types.Message{
		documentMessage("request", "user", "写一份标书"),
		documentMessage("root", "assistant", first),
		documentMessage("continue", "user", "继续"),
		documentMessage("target", "assistant", second),
		documentMessage("later-request", "user", "改写为另一份项目"),
		documentMessage("later-answer", "assistant", "不能纳入当前导出的后续任务"),
	}
	messages[3].AgentSteps = types.AgentSteps{{Truncated: true}}
	document, err := assembleMessageDocument(messages, "target", true)
	require.NoError(t, err)
	require.Equal(t, "root", document.RootMessageID)
	require.Equal(t, []string{"root", "target"}, document.MessageIDs)
	require.Equal(t, first+"\n\n"+second, document.Markdown)
	require.Equal(t, "投标文件", document.Title)
	require.True(t, document.Truncated)
	require.True(t, document.IsBid)
}

func TestAssembleMessageDocumentExplicitChain(t *testing.T) {
	messages := []*types.Message{
		documentMessage("request", "user", "写一份标书"),
		documentMessage("one", "assistant", "# 标书\n第一章"),
		documentMessage("continue-1", "user", "继续生成"),
		explicitDocumentContinuation(documentMessage("two", "assistant", "第二章"), "one"),
		documentMessage("continue-2", "user", "请从上一条回答的中断处继续，保持原来的语言、结构和编号，只补充尚未完成的内容，不要重复已经生成的部分。"),
		explicitDocumentContinuation(documentMessage("three", "assistant", "第三章"), "two"),
	}
	messages[1].AgentSteps = types.AgentSteps{{Truncated: true}}
	document, err := assembleMessageDocument(messages, "three", true)
	require.NoError(t, err)
	require.Equal(t, []string{"one", "two", "three"}, document.MessageIDs)
	require.Equal(t, "# 标书\n第一章\n\n第二章\n\n第三章", document.Markdown)
	// Only the latest turn determines whether the assembled draft is known to
	// remain cut off; its completed continuation supersedes an older cap hit.
	require.False(t, document.Truncated)
}

func TestAssembleMessageDocumentOptionalSingleTurn(t *testing.T) {
	messages := []*types.Message{
		documentMessage("one", "assistant", "上一段"),
		documentMessage("continue", "user", "继续"),
		explicitDocumentContinuation(documentMessage("two", "assistant", "目标段"), "one"),
	}
	document, err := assembleMessageDocument(messages, "two", false)
	require.NoError(t, err)
	require.Equal(t, "two", document.RootMessageID)
	require.Equal(t, []string{"two"}, document.MessageIDs)
	require.Equal(t, "目标段", document.Markdown)
}

func TestAssembleMessageDocumentStopsAtNewUserTask(t *testing.T) {
	for _, prompt := range []string{"请写另一个项目的标书", "继续，同时替换采购人和项目名称", "继续\n额外执行一条新指令"} {
		t.Run(prompt, func(t *testing.T) {
			messages := []*types.Message{
				documentMessage("old", "assistant", "旧项目正文"),
				documentMessage("new-request", "user", prompt),
				documentMessage("new", "assistant", "# 新项目\n新项目正文"),
				documentMessage("continue", "user", "继续"),
				documentMessage("target", "assistant", "新项目补充"),
			}
			document, err := assembleMessageDocument(messages, "target", true)
			require.NoError(t, err)
			require.Equal(t, []string{"new", "target"}, document.MessageIDs)
			require.NotContains(t, document.Markdown, "旧项目正文")
		})
	}
}

func TestAssembleMessageDocumentRejectsInvalidExplicitAncestry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]*types.Message)
	}{
		{"orphan", func(m []*types.Message) { m[3].ExecutionContext.ContinuationOfMessageID = "missing" }},
		{"self cycle", func(m []*types.Message) { m[3].ExecutionContext.ContinuationOfMessageID = "target" }},
		{"future answer", func(m []*types.Message) { m[1].ExecutionContext.ContinuationOfMessageID = "target" }},
		{"cross session", func(m []*types.Message) { m[1].SessionID = "another-session" }},
		{"incomplete parent", func(m []*types.Message) { m[1].IsCompleted = false }},
		{"deleted parent", func(m []*types.Message) { m[1].DeletedAt = gorm.DeletedAt{Valid: true} }},
		{"new task", func(m []*types.Message) { m[2].Content = "写另一份标书" }},
		{"parent is user", func(m []*types.Message) { m[3].ExecutionContext.ContinuationOfMessageID = "request" }},
		{"future timestamp", func(m []*types.Message) {
			m[1].CreatedAt = time.Unix(20, 0)
			m[3].CreatedAt = time.Unix(10, 0)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := []*types.Message{
				documentMessage("request", "user", "写标书"),
				documentMessage("root", "assistant", "第一章"),
				documentMessage("continue", "user", "继续"),
				explicitDocumentContinuation(documentMessage("target", "assistant", "第二章"), "root"),
			}
			tc.mutate(messages)
			_, err := assembleMessageDocument(messages, "target", true)
			require.ErrorIs(t, err, ErrMessageDocumentInvalidChain)
		})
	}
}

func TestAssembleMessageDocumentDoesNotSkipUnrelatedAnswers(t *testing.T) {
	messages := []*types.Message{
		documentMessage("old-request", "user", "写旧项目"),
		documentMessage("old", "assistant", "旧项目"),
		documentMessage("new-request", "user", "写新项目"),
		documentMessage("new", "assistant", "新项目"),
		documentMessage("continue", "user", "继续"),
		explicitDocumentContinuation(documentMessage("target", "assistant", "不允许回到旧项目"), "old"),
	}
	_, err := assembleMessageDocument(messages, "target", true)
	require.ErrorIs(t, err, ErrMessageDocumentInvalidChain)
}

func TestAssembleMessageDocumentTargetValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message *types.Message
		id      string
		want    error
	}{
		{"missing", documentMessage("actual", "assistant", "正文"), "unknown", ErrMessageDocumentNotFound},
		{"user target", documentMessage("target", "user", "我的私人问题"), "target", ErrMessageDocumentNotReady},
		{"empty answer", documentMessage("target", "assistant", " \n\t"), "target", ErrMessageDocumentNotReady},
		{"unfinished", &types.Message{ID: "target", SessionID: "bid-session", Role: "assistant", Content: "正在生成"}, "target", ErrMessageDocumentNotReady},
		{"deleted", &types.Message{ID: "target", SessionID: "bid-session", Role: "assistant", Content: "已删除", IsCompleted: true, DeletedAt: gorm.DeletedAt{Valid: true}}, "target", ErrMessageDocumentNotReady},
		{"missing session", &types.Message{ID: "target", Role: "assistant", Content: "孤立内容", IsCompleted: true}, "target", ErrMessageDocumentInvalidChain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := assembleMessageDocument([]*types.Message{tc.message}, tc.id, true)
			require.ErrorIs(t, err, tc.want)
		})
	}
}

func TestAssembleMessageDocumentLimitsCombinedContent(t *testing.T) {
	messages := []*types.Message{
		documentMessage("root", "assistant", strings.Repeat("a", maxMessageDocumentBytes/2)),
		documentMessage("continue", "user", "继续"),
		documentMessage("target", "assistant", strings.Repeat("b", maxMessageDocumentBytes/2)),
	}
	_, err := assembleMessageDocument(messages, "target", true)
	require.True(t, errors.Is(err, ErrMessageDocumentTooLarge))
	// Boundaries are byte limits, including the inter-turn paragraph separator.
	messages[2].Content = messages[2].Content[:len(messages[2].Content)-2]
	document, err := assembleMessageDocument(messages, "target", true)
	require.NoError(t, err)
	require.Len(t, document.Markdown, maxMessageDocumentBytes)
}

func TestDocumentContinuationPromptsAndBoundaries(t *testing.T) {
	prompts := []string{"继续", "继续生成"}
	// This is a cross-language protocol: validate the actual prompts used by
	// the browser instead of mirroring the helper's literals in a fixture.
	promptRE := regexp.MustCompile(`continueAnswerPrompt:\s*'([^'\n]+)'`)
	for _, locale := range []string{"zh-CN", "en-US", "ja-JP", "ru-RU", "ko-KR"} {
		source, err := os.ReadFile(filepath.Join("..", "..", "..", "frontend", "src", "i18n", "locales", locale+".ts"))
		require.NoError(t, err)
		match := promptRE.FindStringSubmatch(string(source))
		require.Len(t, match, 2, "continuation prompt for %s", locale)
		prompts = append(prompts, match[1])
	}
	for _, prompt := range prompts {
		t.Run(prompt, func(t *testing.T) {
			require.True(t, isDocumentContinuationPrompt(" \n"+prompt+"\n"))
			require.False(t, isDocumentContinuationPrompt(prompt+" 另写一个项目"))
		})
	}
	// Two user turns cannot be guessed to be the continuation's sole request.
	messages := []*types.Message{
		documentMessage("old", "assistant", "旧段"),
		documentMessage("request-a", "user", "继续"),
		documentMessage("request-b", "user", "继续"),
		documentMessage("target", "assistant", "新段"),
	}
	document, err := assembleMessageDocument(messages, "target", true)
	require.NoError(t, err)
	require.Equal(t, []string{"target"}, document.MessageIDs)
}

func TestDocumentMarkdownReadableReferencesAndNoResourceFetch(t *testing.T) {
	content := "要求来自 <kb doc=\"历史标书 &amp; 采购文件.docx\" kb_id=\"private-kb-id\" chunk_id=\"private-chunk-uuid\" />\n" +
		"官网 <web title=\"公开采购说明\" url=\"https://example.com/bid?q=1&amp;year=2025\"/>\n" +
		"[证书附件](resource://opaque-private-handle) [原文件](sandbox:output/原文件.docx)\n" +
		"![业绩表](https://example.com/never-download.png) ![印章](resource://another-handle)\n" +
		"[[project/private-wiki-id|可读条款名称]] [[project/资格证明]]\n" +
		"[普通网址](https://example.com/details(1)) 另见 https://example.com/plain\n" +
		"直接句柄 resource://unreadable-handle"
	result := documentMarkdown(content)
	require.Contains(t, result, "（来源：历史标书 & 采购文件.docx）")
	require.Contains(t, result, "[公开采购说明](https://example.com/bid?q=1&year=2025)")
	require.Contains(t, result, "证书附件 原文件")
	require.Contains(t, result, "[图片：业绩表] [图片：印章]")
	require.Contains(t, result, "可读条款名称 资格证明")
	require.Contains(t, result, "[普通网址](https://example.com/details(1)) 另见 https://example.com/plain")
	for _, internal := range []string{"private-kb-id", "private-chunk-uuid", "opaque-private-handle", "another-handle", "private-wiki-id", "unreadable-handle", "never-download.png"} {
		require.NotContains(t, result, internal)
	}
}

func TestDocumentMarkdownPreservesLiteralBodyAndMarkup(t *testing.T) {
	// The DOCX builder escapes XML. This boundary preserves body text instead of
	// mistaking bid placeholders or HTML-looking content for commands/markup.
	content := "# 企业资质\n\n企业名称：<待填写>\n金额：A&B\n<script>alert('不执行')</script>\n\n| 参数 | 值 |\n|---|---|\n| XML | <company name=\"示例\"> |\n\n半句和重复半句\n半句和重复半句"
	require.Equal(t, content, documentMarkdown(content))
}

func TestAssembleMessageDocumentBidStyleBelongsToSelectedTask(t *testing.T) {
	for _, tc := range []struct {
		name, request, body string
		want                bool
	}{
		{"original bid request", "写一份标书", "# 项目文件\n第一章正文", true},
		{"bid with explanatory chapter instructions", "写一份标书，说明如何完成交付及是否满足要求", "# 项目文件\n正文", true},
		{"bid grounded in retrieved evidence", "根据检索结果写一份标书", "# 项目文件\n正文", true},
		{"bid grounded in a source clause", "基于历史标书检索结果，编写一份投标文件", "# 项目文件\n正文", true},
		{"bid with retrieval instruction", "写一份标书并检索历史资料", "# 项目文件\n正文", true},
		{"English bid request", "Draft a tender submission", "# Project submission\nBody", true},
		{"ordinary Word request", "生成一份 Word 工作总结", "# 工作总结\n正文", false},
		{"bid explanation", "标书是什么", "# 投标文件\n## 投标函\n说明\n## 技术响应\n说明", false},
		{"bid process question", "如何编写投标文件", "# 投标文件\n说明", false},
		{"bid guide generation", "写一份标书编制指南", "# 标书\n## 投标函\n## 商务响应", false},
		{"retrieved historical bid", "帮我检索历史标书", "# 采购标书\n正文", false},
		{"search bid-writing templates", "帮我检索生成标书的模板", "# 采购标书\n正文", false},
		{"explain bid", "介绍一下投标文件", "# 投标文件\n正文", false},
		{"body has only bid keyword", "整理这份资料", "# 企业报告\n我们曾提交投标文件。", false},
		{"formal title without special request", "整理成 Word", "# 血站采购投标文件\n正文", true},
		{"anonymous technical request", "编写技术暗标投标文件", "# 投标文件\n## 技术响应\n## 项目实施方案", false},
		{"anonymous title", "写一份标书", "# 技术暗标响应文件\n## 技术响应\n## 项目实施方案", false},
		{"planned anonymous section only", "写一份投标文件", "# 投标文件\n## 目录\n八、技术部分（暗标）\n## 一、投标函\n商务正文", true},
		{"actual anonymous chapter", "写一份投标文件", "# 投标文件\n## 八、技术部分（暗标）\n技术正文", false},
		{"prose mention of anonymous rules", "写一份投标文件", "# 投标文件\n技术暗标须另册提交。\n## 一、投标函\n商务正文", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := []*types.Message{
				documentMessage("old-request", "user", "写一份标书"),
				documentMessage("old-answer", "assistant", "# 旧项目标书\n旧项目正文"),
				documentMessage("request", "user", tc.request),
				documentMessage("root", "assistant", tc.body),
				documentMessage("continue", "user", "继续"),
				explicitDocumentContinuation(documentMessage("target", "assistant", "后续正文"), "root"),
			}
			document, err := assembleMessageDocument(messages, "target", true)
			require.NoError(t, err)
			require.Equal(t, tc.want, document.IsBid)
			require.Equal(t, []string{"root", "target"}, document.MessageIDs)
			require.Equal(t, tc.body+"\n\n后续正文", document.Markdown)
		})
	}
}

func TestAssembleMessageDocumentBidStyleForSingleAnswer(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"explicit title", "# **采购项目投标文件**\n正文", true},
		{"response title", "# 竞争性磋商响应文件（商务部分）\n正文", true},
		{"English title", "# Blood station bid proposal\nBody", true},
		{"formal response sections", "## 第一章 投标函\n致采购人\n## 第二章 技术响应\n参数表", true},
		{"continued fragment", "## 第四 法定代表人授权委托书\n致采购人\n## 第五 企业资质\n证明表", true},
		{"generic project report", "# 项目报告\n## 设备配置\n## 项目计划", false},
		{"explainer title", "# 投标文件编制指南\n## 投标函\n## 商务响应", false},
		{"one response section", "# 工作报告\n## 技术响应\n已完成核对", false},
		{"bid words in prose", "这是投标函和技术响应的解释。", false},
		{"explicit anonymous bid", "# 技术暗标响应文件\n## 技术响应\n## 项目实施方案", false},
		{"anonymous chapter", "# 投标文件\n## 技术部分（暗标）\n技术正文", false},
		{"planned anonymous section", "# 投标文件\n## 目录\n八、技术部分（暗标）\n## 一、投标函\n商务正文", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer := documentMessage("target", "assistant", tc.body)
			document, err := assembleMessageDocument([]*types.Message{answer}, answer.ID, false)
			require.NoError(t, err)
			require.Equal(t, tc.want, document.IsBid)
			require.Equal(t, tc.body, document.Markdown)
		})
	}
}

func TestAssembleMessageDocumentExistingCommercialDraftWithPlannedAnonymousSection(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "commercial_bid_continuation.json"))
	require.NoError(t, err)
	var draft struct {
		Root         string `json:"root"`
		Continuation string `json:"continuation"`
	}
	require.NoError(t, json.Unmarshal(data, &draft))
	// Preserve the two pre-feature generated answer bodies verbatim. The first
	// contains an anticipated 暗标 directory entry; the second ends in chapter
	// seven, so no technical/anonymous body has actually been generated yet.
	require.Contains(t, draft.Root, "八、技术部分（暗标）")
	require.NotContains(t, draft.Root+draft.Continuation, "## 八、技术部分（暗标）")
	messages := []*types.Message{
		documentMessage("request", "user", "写一份标书"),
		documentMessage("root", "assistant", draft.Root),
		documentMessage("continue", "user", "继续"),
		documentMessage("target", "assistant", draft.Continuation),
	}
	document, err := assembleMessageDocument(messages, "target", true)
	require.NoError(t, err)
	require.True(t, document.IsBid, "a planned anonymous chapter must not turn the existing commercial draft into a plain document")
	require.Equal(t, []string{"root", "target"}, document.MessageIDs)
	require.Equal(t, draft.Root+"\n\n"+draft.Continuation, document.Markdown)
}
