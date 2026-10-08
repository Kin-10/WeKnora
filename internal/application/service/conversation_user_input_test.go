package service

import (
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestConversationUserInputFollowsCustomAgentOptIn(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		svc := &sessionService{
			cfg:                   &config.Config{},
			webSearchProviderRepo: &sharedAgentWebSearchRepo{},
		}
		agent := &types.CustomAgent{TenantID: 1, Config: types.CustomAgentConfig{
			UserInputEnabled: enabled,
			SystemPrompt:     "Custom workflow",
		}}
		req := &types.QARequest{Session: &types.Session{ID: "session", TenantID: 1}, CustomAgent: agent}
		cfg, err := svc.buildAgentConfig(t.Context(), req, &types.Tenant{ID: 1}, 1)
		require.NoError(t, err)
		require.Equal(t, enabled, cfg.UserInputEnabled)
		require.Equal(t, "Custom workflow", cfg.SystemPrompt, "keep saved prompt text intact")

		cm := &types.ChatManage{}
		svc.applyAgentOverridesToChatManage(t.Context(), agent, cm)
		require.Equal(t, enabled, cm.UserInputEnabled)
		require.Equal(t, enabled, cm.Clone().UserInputEnabled, "retrieval clones must retain the gate")
		require.Equal(t, "Custom workflow", cm.SummaryConfig.Prompt)

		// Even the no-evidence fallback must ask for essential facts rather than
		// inventing them; legacy agents keep the existing fallback instructions.
		messages := buildFallbackMessages(cm, "fallback instruction")
		require.Equal(t, enabled, containsUserInputPrompt(messages[0].Content))
	}
}

func TestConversationUserInputFallbackWithoutCustomPrompt(t *testing.T) {
	cm := &types.ChatManage{PipelineRequest: types.PipelineRequest{UserInputEnabled: true}}
	messages := buildFallbackMessages(cm, "")
	require.Equal(t, "system", messages[0].Role)
	require.Contains(t, messages[0].Content, types.ConversationalUserInputPrompt)
}

func containsUserInputPrompt(prompt string) bool {
	return strings.Contains(prompt, types.ConversationalUserInputPrompt)
}
