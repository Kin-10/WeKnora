package session

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestUserInputRequestCannotExportOrBecomeAContinuationParent(t *testing.T) {
	for _, content := range []string{
		"请先确认项目。\n\n```weknora-input\n{\"id\":\"project\",\"title\":\"项目\",\"questions\":[{\"id\":\"name\",\"label\":\"项目名称\",\"type\":\"text\"}]}\n```",
		"请先确认项目。\n\n```weknora-input\n{",
	} {
		answer := documentMessage("answer", "assistant", content)
		h, _, _, files, router := documentFixture(answer)
		require.False(t, documentAnswerReady(answer))
		response := postMessageDocument(router, answer.ID, `{"format":"docx"}`)
		require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
		require.Zero(t, files.saves)

		var snapshot types.MessageExecutionContext
		err := h.configureDocumentRequest(context.Background(), &types.Session{ID: "bid-session"},
			&CreateKnowledgeQARequest{Query: "继续", ContinuationOfMessageID: answer.ID}, &snapshot)
		require.Error(t, err, "a request for missing facts is not a chapter to continue")
	}
}

func TestAutomaticCompletionPersistsUserInputWithoutCreatingWord(t *testing.T) {
	answer := documentMessage("answer", "assistant", "请补充资料。\n\n```weknora-input\n{\"unfinished\":true}\n```")
	answer.ExecutionContext.DocumentRequested = true
	h, _, store, files, _ := documentFixture(documentMessage("write", "user", "写一份标书"), answer)
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(42))
	stream := &completionEventRecorder{}
	live := copyDocumentMessage(answer)
	streamHandler := NewAgentStreamHandler(ctx, "bid-session", "answer", "request", 42, time.Time{}, live, stream, nil, nil, nil, nil)
	require.NoError(t, streamHandler.handleComplete(ctx, event.Event{Data: event.AgentCompleteData{MessageID: "answer"}}))
	h.completeStreamAssistantMessage(ctx, &sseStreamContext{assistantMessage: live, streamHandler: streamHandler}, "写一份标书", "write")
	require.Equal(t, answer.Content, store.messages[1].Content)
	require.True(t, store.messages[1].IsCompleted)
	require.Empty(t, store.messages[1].Artifacts)
	require.Zero(t, files.saves)
	require.Len(t, stream.events, 1)
	_, hasArtifacts := stream.events[0].Data["artifacts"]
	require.False(t, hasArtifacts)
}
