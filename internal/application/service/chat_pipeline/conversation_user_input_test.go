package chatpipeline

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestConversationUserInputPromptRequiresOptInAndSurvivesIntentOverride(t *testing.T) {
	cm := &types.ChatManage{}
	cm.SummaryConfig.Prompt = "Custom workflow"
	cm.UserContent = "Generate a bid"
	without := prepareMessagesWithHistory(cm)
	require.NotContains(t, without[0].Content, "weknora-input")

	cm.UserInputEnabled = true
	cm.SystemPromptOverride = "Intent-specific workflow"
	with := prepareMessagesWithHistory(cm)
	require.Contains(t, with[0].Content, "Intent-specific workflow")
	require.Contains(t, with[0].Content, types.ConversationalUserInputPrompt)
	require.Equal(t, cm.UserContent, with[1].Content)
	cm.UserInputEnabled = false
	cm.SystemPromptOverride = ""
	require.Equal(t, without, prepareMessagesWithHistory(cm), "legacy agents retain the original prompt")
}
