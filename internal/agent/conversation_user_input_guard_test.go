package agent

import (
	"context"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const guardInputCard = "请确认本次项目。\n```weknora-input\n" +
	`{"id":"company","title":"确认投标人","questions":[{"id":"name","label":"企业名称","type":"text","required":true}]}` + "\n```"

func TestExecuteLoopUserInputCardStopsToolsAndFurtherModelCalls(t *testing.T) {
	for _, reason := range []string{"tool_calls", "stop", "length"} {
		t.Run(reason, func(t *testing.T) {
			call := types.LLMToolCall{ID: "write-call", Type: "function", Function: types.FunctionCall{Name: "write_file", Arguments: `{"content":"must not be written"}`}}
			model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{
				{ResponseType: types.ResponseTypeAnswer, Content: guardInputCard},
				{ResponseType: types.ResponseTypeToolCall, Data: map[string]interface{}{
					"tool_call_id": call.ID, "tool_name": call.Function.Name, "arguments": map[string]any{"content": "must not be written"},
				}},
				{ToolCalls: []types.LLMToolCall{call}, FinishReason: reason, Done: true,
					Usage: &types.TokenUsage{PromptTokens: 40, CompletionTokens: 20, TotalTokens: 60}},
			}}}}
			engine := newTestEngine(t, model, func(config *types.AgentConfig) { config.UserInputEnabled = true })
			registry := agenttools.NewToolRegistry()
			tool := newCountingTool("write_file")
			registry.RegisterTool(tool)
			engine.toolRegistry = registry
			recorder := &truncationRecorder{}
			recorder.attach(engine.eventBus)
			var refused []event.AgentToolResultData
			engine.eventBus.On(event.EventAgentToolResult, func(_ context.Context, evt event.Event) error {
				refused = append(refused, evt.Data.(event.AgentToolResultData))
				return nil
			})
			state := &types.AgentState{}
			_, err := engine.executeLoop(context.Background(), state, "生成标书", emptyMessages(), nil, "session", "message")
			require.NoError(t, err)
			require.Equal(t, 1, model.callCount)
			require.Zero(t, tool.calls, "the same response cannot ask for facts and write a file")
			require.True(t, state.IsComplete)
			require.Equal(t, guardInputCard, state.FinalAnswer)
			require.Len(t, state.RoundSteps, 1)
			require.Empty(t, state.RoundSteps[0].ToolCalls, "unexecuted tools must not become performed work")
			require.Equal(t, 60, state.TurnUsage.TotalTokens)
			require.Len(t, refused, 1, "a pending provider notification must be resolved")
			require.False(t, refused[0].Success)
			require.Contains(t, refused[0].Error, "not executed")
			require.Equal(t, reason == "length", recorder.doneEvent(t).Truncated)
			closed := 0
			for _, evt := range recorder.snapshot() {
				if evt.Done {
					closed++
				}
			}
			require.Equal(t, 1, closed)
		})
	}
}

func TestExecuteLoopUserInputCardDoesNotConsumeLoopEndSteering(t *testing.T) {
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{
		{ResponseType: types.ResponseTypeAnswer, Content: guardInputCard, Done: true, FinishReason: "stop"},
	}}}}
	engine := newTestEngine(t, model, func(config *types.AgentConfig) { config.UserInputEnabled = true })
	sink := &delayedSteerSink{fakeSteerSink: fakeSteerSink{queued: []map[string]interface{}{steerEntry("later", "补充说明")}}, hideUntil: 2}
	engine.SetSteerSink(sink)
	state := &types.AgentState{}
	_, err := engine.executeLoop(context.Background(), state, "生成标书", emptyMessages(), nil, "session", "message")
	require.NoError(t, err)
	require.Equal(t, 1, model.callCount)
	require.Equal(t, guardInputCard, state.FinalAnswer)
	require.Empty(t, sink.persisted, "a card ends this turn; queued steering remains for the next turn")
}

func TestExecuteLoopUserInputGuardKeepsDefaultAndCodeExamplesCompatible(t *testing.T) {
	for _, example := range []struct {
		name, content string
		enabled       bool
	}{
		{name: "default off", content: guardInputCard},
		{name: "nested example", content: "````markdown\n" + guardInputCard + "\n````", enabled: true},
		{name: "quoted example", content: "> ```weknora-input\n> {}\n> ```", enabled: true},
	} {
		t.Run(example.name, func(t *testing.T) {
			model := &mockChat{responses: []mockResponse{
				{chunks: []types.StreamResponse{{Content: example.content, Done: true, FinishReason: "tool_calls", ToolCalls: []types.LLMToolCall{{
					ID: "ordinary", Type: "function", Function: types.FunctionCall{Name: "read_file", Arguments: `{}`},
				}}}}},
				{chunks: []types.StreamResponse{{Content: "正常工具后回答", Done: true, FinishReason: "stop"}}},
			}}
			engine := newTestEngine(t, model, func(config *types.AgentConfig) { config.UserInputEnabled = example.enabled })
			registry := agenttools.NewToolRegistry()
			tool := newCountingTool("read_file")
			registry.RegisterTool(tool)
			engine.toolRegistry = registry
			state := &types.AgentState{}
			_, err := engine.executeLoop(context.Background(), state, "普通工具任务", emptyMessages(), nil, "session", "message")
			require.NoError(t, err)
			require.Equal(t, 2, model.callCount)
			require.Equal(t, 1, tool.calls)
			require.Equal(t, "正常工具后回答", state.FinalAnswer)
		})
	}
}
