package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/bidformat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func tenderFormattingServiceFixture(filename, quote string) *types.TenderFormattingSnapshot {
	return &types.TenderFormattingSnapshot{
		Version: 1, SourceFiles: []string{filename}, SourceError: "internal source error resource://private-tender",
		Result: bidformat.Result{Rules: []bidformat.Rule{{Scope: bidformat.ScopeBusiness, Target: "body", Property: "font_family", Value: "宋体", Quote: quote,
			Source: bidformat.Source{ID: "private-source-id", Name: filename, SHA256: "private-provenance-hash", Page: 5}}}},
		Info: &types.DocumentFormattingInfo{Mode: "tender", Scope: "business", SourceFiles: []string{filename}, Summary: []string{"正文宋体"}},
	}
}

func TestTenderFormattingHistoryProjectsPublicInfoWithoutMutatingSnapshot(t *testing.T) {
	snapshot := tenderFormattingServiceFixture("当前项目招标文件.pdf", "private-original-source-quote")
	records := []*types.Message{
		{ID: "request", SessionID: "session", Role: "user", ExecutionContext: types.MessageExecutionContext{TenderFormatting: snapshot}},
		{ID: "answer", SessionID: "session", Role: "assistant", Content: "正文。", IsCompleted: true, ExecutionContext: types.MessageExecutionContext{TenderFormatting: snapshot}},
		{ID: "legacy", SessionID: "session", Role: "assistant", DocumentFormatting: &types.DocumentFormattingInfo{Mode: "stale"}},
	}
	service := &messageService{sessionRepo: &agentSourceHistorySessions{}, messageRepo: &agentSourceHistoryMessages{messages: records}}
	ctx := types.WithExecutionTenant(context.Background(), 1)
	reads := map[string]func() ([]*types.Message, error){
		"page":   func() ([]*types.Message, error) { return service.GetMessagesBySession(ctx, "session", 1, 20) },
		"recent": func() ([]*types.Message, error) { return service.GetRecentMessagesBySession(ctx, "session", 20) },
		"before": func() ([]*types.Message, error) {
			return service.GetMessagesBySessionBeforeTime(ctx, "session", time.Now(), 20)
		},
		"single": func() ([]*types.Message, error) {
			message, err := service.GetMessage(ctx, "session", "answer")
			return []*types.Message{message}, err
		},
	}
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			messages, err := read()
			require.NoError(t, err)
			var answer *types.Message
			for _, message := range messages {
				if message.Role == "assistant" && message.ID == "answer" {
					answer = message
				} else {
					require.Nil(t, message.DocumentFormatting, "user and legacy rows must not receive a task status")
				}
			}
			require.NotNil(t, answer)
			require.Equal(t, snapshot.Info, answer.DocumentFormatting)
			require.NotSame(t, records[1], answer)
			require.NotSame(t, snapshot.Info, answer.DocumentFormatting, "public projection must not alias repository snapshot state")
			data, err := json.Marshal(answer)
			require.NoError(t, err)
			for _, private := range []string{"execution_context", "tender_formatting", "private-original-source-quote", "private-source-id", "private-provenance-hash", "resource://private-tender"} {
				require.NotContains(t, string(data), private)
			}
			answer.DocumentFormatting.Mode = "changed"
			answer.DocumentFormatting.SourceFiles[0] = "different-project.pdf"
			answer.DocumentFormatting.Summary[0] = "changed style"
			require.Equal(t, "tender", snapshot.Info.Mode)
			require.Equal(t, []string{"当前项目招标文件.pdf"}, snapshot.Info.SourceFiles)
			require.Equal(t, []string{"正文宋体"}, snapshot.Info.Summary)
			require.Nil(t, records[1].DocumentFormatting, "history reads must leave the transient repository field untouched")
		})
	}
	projected := projectMessageAgentSources([]*types.Message{nil}, 1)
	require.Len(t, projected, 1)
	require.Nil(t, projected[0])
	require.Nil(t, projectMessageAgentSources(nil, 1))
}

func TestTenderFormattingForkKeepsEachTaskProfileAndRemapsOnlyItsContinuation(t *testing.T) {
	profileA := tenderFormattingServiceFixture("项目甲招标文件.pdf", "项目甲字号要求")
	profileB := tenderFormattingServiceFixture("项目乙招标文件.pdf", "项目乙字号要求")
	history := []*types.Message{
		{ID: "request-a", SessionID: "source", Role: "user", Content: "生成项目甲标书"},
		{ID: "answer-a", SessionID: "source", Role: "assistant", IsCompleted: true, ExecutionContext: types.MessageExecutionContext{TenderFormatting: profileA, DocumentRequested: true}},
		{ID: "continue-a", SessionID: "source", Role: "user", Content: "继续"},
		{ID: "continued-a", SessionID: "source", Role: "assistant", IsCompleted: true, ExecutionContext: types.MessageExecutionContext{TenderFormatting: profileA, DocumentRequested: true, ContinuationOfMessageID: "answer-a"}},
		{ID: "request-b", SessionID: "source", Role: "user", Content: "生成项目乙标书"},
		{ID: "answer-b", SessionID: "source", Role: "assistant", IsCompleted: true, ExecutionContext: types.MessageExecutionContext{TenderFormatting: profileB, DocumentRequested: true}},
		{ID: "continue-b", SessionID: "source", Role: "user", Content: "继续"},
		{ID: "continued-b", SessionID: "source", Role: "assistant", IsCompleted: true, ExecutionContext: types.MessageExecutionContext{TenderFormatting: profileB, DocumentRequested: true, ContinuationOfMessageID: "answer-b"}},
	}
	before, err := json.Marshal(history)
	require.NoError(t, err)
	copies := copyMessagesInto("forked-session", history)
	require.Len(t, copies, len(history))
	require.Equal(t, copies[1].ID, copies[3].ExecutionContext.ContinuationOfMessageID)
	require.Equal(t, copies[5].ID, copies[7].ExecutionContext.ContinuationOfMessageID)
	require.NotEqual(t, copies[1].ID, copies[7].ExecutionContext.ContinuationOfMessageID, "project B must not inherit project A's continuation root")
	for _, index := range []int{1, 3, 5, 7} {
		copy, original := copies[index], history[index]
		require.Equal(t, "forked-session", copy.SessionID)
		require.NotEqual(t, original.ID, copy.ID)
		require.Equal(t, original.ExecutionContext.TenderFormatting, copy.ExecutionContext.TenderFormatting)
		require.NotSame(t, original.ExecutionContext.TenderFormatting, copy.ExecutionContext.TenderFormatting, "fork formatting state must not share mutable source profile storage")
		require.True(t, copy.ExecutionContext.DocumentRequested)
	}
	require.Empty(t, copies[5].ExecutionContext.ContinuationOfMessageID, "a new project remains a new writing task")
	projected := projectMessageAgentSources(copies, 1)
	require.Equal(t, []string{"项目甲招标文件.pdf"}, projected[3].DocumentFormatting.SourceFiles)
	require.Equal(t, []string{"项目乙招标文件.pdf"}, projected[7].DocumentFormatting.SourceFiles)
	// Changing a fork's stored profile must not change its parent or a sibling
	// copied turn that happened to point at the same frozen source profile.
	copies[3].ExecutionContext.TenderFormatting.Info.SourceFiles[0] = "fork-only.pdf"
	copies[3].ExecutionContext.TenderFormatting.Result.Rules[0].Quote = "fork-only evidence"
	require.Equal(t, "项目甲招标文件.pdf", profileA.Info.SourceFiles[0])
	require.Equal(t, "项目甲字号要求", profileA.Result.Rules[0].Quote)
	require.Equal(t, "项目甲招标文件.pdf", copies[1].ExecutionContext.TenderFormatting.Info.SourceFiles[0])
	require.Equal(t, "项目乙招标文件.pdf", copies[7].ExecutionContext.TenderFormatting.Info.SourceFiles[0])
	after, err := json.Marshal(history)
	require.NoError(t, err)
	require.Equal(t, before, after, "copying and editing fork formatting must not mutate original history")
	// Message JSON intentionally omits ExecutionContext; verify the internal
	// parent profile too, rather than relying only on public JSON equality.
	require.Equal(t, "answer-a", history[3].ExecutionContext.ContinuationOfMessageID)
	require.Equal(t, "answer-b", history[7].ExecutionContext.ContinuationOfMessageID)
}

func TestTenderFormattingForkDoesNotRemapForeignTaskAncestor(t *testing.T) {
	foreign := &types.Message{ID: "foreign-answer", SessionID: "other-session", Role: "assistant", IsCompleted: true,
		ExecutionContext: types.MessageExecutionContext{TenderFormatting: tenderFormattingServiceFixture("其他项目.pdf", "其他项目规则")}}
	child := &types.Message{ID: "current-answer", SessionID: "source", Role: "assistant", IsCompleted: true,
		ExecutionContext: types.MessageExecutionContext{TenderFormatting: tenderFormattingServiceFixture("当前项目.pdf", "当前项目规则"), ContinuationOfMessageID: "foreign-answer"}}
	copies := copyMessagesInto("fork", []*types.Message{foreign, child})
	require.Equal(t, "foreign-answer", copies[1].ExecutionContext.ContinuationOfMessageID, "an invalid edge remains invalid instead of silently rebinding to another task")
	for _, candidate := range copies {
		require.NotEqual(t, candidate.ID, copies[1].ExecutionContext.ContinuationOfMessageID)
	}
	require.Equal(t, []string{"当前项目.pdf"}, copies[1].ExecutionContext.TenderFormatting.SourceFiles)
	require.Equal(t, "当前项目规则", copies[1].ExecutionContext.TenderFormatting.Result.Rules[0].Quote)
	data, err := json.Marshal(projectMessageAgentSources(copies, 1)[1])
	require.NoError(t, err)
	require.NotContains(t, string(data), "其他项目.pdf")
}
