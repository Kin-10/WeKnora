package agent

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestConversationUserInputPromptRequiresAgentOptIn(t *testing.T) {
	engine := newTestEngine(t, nil)
	without := engine.buildSystemPrompt(t.Context())
	require.NotContains(t, without, "weknora-input")
	engine.config.UserInputEnabled = true
	require.Contains(t, engine.buildSystemPrompt(t.Context()), types.ConversationalUserInputPrompt)
	engine.config.UserInputEnabled = false
	require.Equal(t, without, engine.buildSystemPrompt(t.Context()))
}
