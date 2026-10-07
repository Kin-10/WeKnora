package session

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestLastRequestStatePreservesResolvedSharedBuiltinSource(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		resolvedTenant, wantSource uint64
	}{
		{"own", 0, 0},
		{"own resolved tenant", 1, 0},
		{"shared builtin", 84, 84},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &reasoningSessionStub{}
			h := &Handler{sessionService: stub}
			rc := &qaRequestContext{
				sessionID: "session", session: &types.Session{ID: "session", TenantID: 1},
				reqAgentID: types.BuiltinSmartReasoningID, reqAgentEnabled: true,
				effectiveTenantID: tc.resolvedTenant,
				customAgent: &types.CustomAgent{ID: types.BuiltinSmartReasoningID, TenantID: 84,
					Config: types.CustomAgentConfig{AgentMode: types.AgentModeSmartReasoning}},
			}
			h.persistLastRequestState(context.Background(), rc, qaModeAgent)
			require.NotNil(t, stub.state)
			require.Equal(t, tc.wantSource, stub.state.AgentSourceTenantID)
			raw, err := stub.state.Value()
			require.NoError(t, err)
			var restored types.SessionLastRequestState
			require.NoError(t, restored.Scan(raw))
			require.Equal(t, tc.wantSource, restored.AgentSourceTenantID)
			require.Equal(t, types.BuiltinSmartReasoningID, restored.AgentID)
			var data map[string]any
			require.NoError(t, json.Unmarshal(raw.([]byte), &data))
			if tc.wantSource == 0 {
				require.NotContains(t, data, "agent_source_tenant_id")
			} else {
				require.Equal(t, float64(tc.wantSource), data["agent_source_tenant_id"])
			}
		})
	}
}

func TestLastRequestStateLegacyJSONHasNoSharedSource(t *testing.T) {
	var state types.SessionLastRequestState
	require.NoError(t, state.Scan(`{"agent_id":"builtin-quick-answer","agent_enabled":false,"knowledge_base_ids":["kb"]}`))
	require.Zero(t, state.AgentSourceTenantID)
	require.Equal(t, types.BuiltinQuickAnswerID, state.AgentID)
	require.Equal(t, []string{"kb"}, state.KnowledgeBaseIDs)
}
