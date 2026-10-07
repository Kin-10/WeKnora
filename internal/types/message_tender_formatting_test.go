package types

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/bidformat"
	"github.com/stretchr/testify/require"
)

func tenderFormattingTypesFixture() *TenderFormattingSnapshot {
	source := bidformat.Source{ID: "authorized-tender-42", Name: "本次招标文件.pdf", SHA256: strings.Repeat("a", 64), Page: 81}
	rule := bidformat.Rule{Scope: bidformat.ScopeTechnical, Target: "body", Property: "font_size_half_points", Value: "28", Source: source, Quote: "标题及正文使用宋体四号字。"}
	return &TenderFormattingSnapshot{
		Version: 1, SourceFiles: []string{source.Name},
		Result:      bidformat.Result{Rules: []bidformat.Rule{rule}, Issues: []bidformat.Issue{{Code: "unsupported", Scope: bidformat.ScopeTechnical, Property: "cover_template", Source: &source, Quote: "封面按附件格式。", Message: "需要招标文件指定封面。"}}},
		SourceError: "内部源读取错误：resource://internal-private-source",
		Info:        &DocumentFormattingInfo{Mode: "tender", Scope: "technical", SourceFiles: []string{source.Name}, Summary: []string{"宋体四号"}, Warning: "指定封面需要原始模板。"},
	}
}

func TestTenderFormattingExecutionContextValueScanRetainsFrozenRules(t *testing.T) {
	original := MessageExecutionContext{
		TenderFormatting: tenderFormattingTypesFixture(), ContinuationOfMessageID: "root-answer", DocumentRequested: true,
		KnowledgeIDs: []string{"selected-current-tender"}, AgentConfigHash: "recorded-agent-hash", Locale: "zh-CN",
	}
	stored, err := original.Value()
	require.NoError(t, err)
	data, ok := stored.([]byte)
	require.True(t, ok, "existing JSONB persistence must keep the serialized snapshot")
	for name, value := range map[string]any{"bytes": data, "string": string(data)} {
		t.Run(name, func(t *testing.T) {
			var decoded MessageExecutionContext
			require.NoError(t, decoded.Scan(value))
			require.Equal(t, original, decoded)
			require.Equal(t, "root-answer", decoded.ContinuationOfMessageID)
			require.Equal(t, "28", decoded.TenderFormatting.Result.Rules[0].Value)
			require.Equal(t, 81, decoded.TenderFormatting.Result.Rules[0].Source.Page)
			require.Empty(t, decoded.TenderFormatting.Result.Rules[0].Source.Text, "frozen evidence retains provenance, not the full tender")
			decoded.TenderFormatting.Result.Rules[0].Value = "changed"
			decoded.TenderFormatting.Info.SourceFiles[0] = "changed.pdf"
			require.Equal(t, "28", original.TenderFormatting.Result.Rules[0].Value, "decoded rule mutation must not alter the original")
			require.Equal(t, "本次招标文件.pdf", original.TenderFormatting.Info.SourceFiles[0])
		})
	}
}

func TestTenderFormattingExecutionContextScanLegacyAndInvalidValues(t *testing.T) {
	for name, value := range map[string]any{"null": nil, "unknown driver type": 42} {
		t.Run(name, func(t *testing.T) {
			context := MessageExecutionContext{TenderFormatting: tenderFormattingTypesFixture(), DocumentRequested: true}
			require.NoError(t, context.Scan(value))
			require.Equal(t, MessageExecutionContext{}, context)
		})
	}
	var legacy MessageExecutionContext
	require.NoError(t, legacy.Scan(`{"document_requested":true,"knowledge_ids":["legacy-file"],"web_search_enabled":false}`))
	require.True(t, legacy.DocumentRequested)
	require.Nil(t, legacy.TenderFormatting)
	require.Equal(t, []string{"legacy-file"}, legacy.KnowledgeIDs)
	require.Error(t, legacy.Scan(`{"tender_formatting":`), "invalid JSON must not produce a usable formatting snapshot")
}

func TestTenderFormattingMessageJSONExposesOnlyPublicInfo(t *testing.T) {
	snapshot := tenderFormattingTypesFixture()
	message := Message{
		ID: "answer", SessionID: "authorized-session", Role: "assistant", Content: "正文。", IsCompleted: true,
		ExecutionContext:   MessageExecutionContext{TenderFormatting: snapshot, ContinuationOfMessageID: "private-root-id", KnowledgeIDs: []string{"private-knowledge-id"}},
		DocumentFormatting: snapshot.Info,
	}
	data, err := json.Marshal(message)
	require.NoError(t, err)
	var public map[string]any
	require.NoError(t, json.Unmarshal(data, &public))
	require.NotContains(t, public, "execution_context")
	info, ok := public["document_formatting"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "tender", info["mode"])
	require.Equal(t, "technical", info["scope"])
	require.Equal(t, []any{"本次招标文件.pdf"}, info["source_files"])
	require.Equal(t, []any{"宋体四号"}, info["summary"])
	for _, marker := range []string{"resource://internal-private-source", "authorized-tender-42", "private-root-id", "private-knowledge-id", "标题及正文使用宋体四号字。", strings.Repeat("a", 64), "font_size_half_points", "cover_template", "tender_formatting"} {
		require.NotContains(t, string(data), marker, "public history must not leak frozen source evidence or execution bindings")
	}
	message.DocumentFormatting = nil
	data, err = json.Marshal(message)
	require.NoError(t, err)
	require.NotContains(t, string(data), "document_formatting", "legacy answers omit the new status")
	field, ok := reflect.TypeOf(Message{}).FieldByName("DocumentFormatting")
	require.True(t, ok)
	require.Equal(t, "-", field.Tag.Get("gorm"), "public projection must never become a second database source of truth")
}
