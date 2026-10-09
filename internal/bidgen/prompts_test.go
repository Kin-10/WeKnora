package bidgen

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The planning prompt must ask the user to choose the drafting scope before
// outlining whenever the tender splits into 商务标/技术标 or multiple lots,
// instead of silently planning one full document.
func TestPlanningPromptRequestsScopeSelection(t *testing.T) {
	task := &Task{State: State{Phase: PhasePlanning}}
	request := generationRequest(task)
	require.Equal(t, PhasePlanning, request.Phase)
	prompt := request.Prompt
	for _, want := range []string{
		"商务标/技术标",
		"包段",
		"weknora-input 卡片",
		"用户提问与已补充答复均未明确本次生成范围",
		"本轮不要输出目录计划",
		"标题须注明包段与范围",
		"一个任务只生成一册",
	} {
		require.True(t, strings.Contains(prompt, want), "planning prompt missing %q", want)
	}
	// A task that already collected user replies keeps the same instruction:
	// replies are appended verbatim and the model must not re-ask them.
	task.State.UserReplies = []UserReply{{Text: "只生成第1包技术标"}}
	replyJSON, err := json.Marshal(task.State.UserReplies)
	require.NoError(t, err)
	require.True(t, strings.Contains(generationRequest(task).Prompt, string(replyJSON)))
}
