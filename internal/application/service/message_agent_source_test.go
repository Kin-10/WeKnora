package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type agentSourceHistorySessions struct {
	interfaces.SessionRepository
	ownerTenantID uint64
}

func (r *agentSourceHistorySessions) Get(_ context.Context, tenantID uint64, _, sessionID string) (*types.Session, error) {
	if r.ownerTenantID != 0 {
		tenantID = r.ownerTenantID
	}
	return &types.Session{ID: sessionID, TenantID: tenantID, UserID: "user"}, nil
}

func (*agentSourceHistorySessions) GetIMPlatform(context.Context, uint64, string) (string, error) {
	return "", nil
}

type agentSourceHistoryMessages struct {
	interfaces.MessageRepository
	messages []*types.Message
}

func (r *agentSourceHistoryMessages) GetMessage(context.Context, string, string) (*types.Message, error) {
	return r.messages[1], nil
}
func (r *agentSourceHistoryMessages) GetMessagesBySession(context.Context, string, int, int) ([]*types.Message, error) {
	return r.messages, nil
}
func (r *agentSourceHistoryMessages) GetRecentMessagesBySession(context.Context, string, int) ([]*types.Message, error) {
	return r.messages, nil
}
func (r *agentSourceHistoryMessages) GetMessagesBySessionBeforeTime(context.Context, string, time.Time, int) ([]*types.Message, error) {
	return r.messages, nil
}

func TestMessageHistoryProjectsOnlyBorrowedAgentSource(t *testing.T) {
	records := []*types.Message{
		{ID: "own", SessionID: "session", Role: "assistant", AgentID: types.BuiltinSmartReasoningID, AgentTenantID: 1},
		{ID: "shared", SessionID: "session", Role: "assistant", AgentID: types.BuiltinSmartReasoningID, AgentTenantID: 84},
		{ID: "legacy", SessionID: "session", Role: "assistant", AgentID: types.BuiltinSmartReasoningID},
	}
	s := &messageService{
		sessionRepo: &agentSourceHistorySessions{}, messageRepo: &agentSourceHistoryMessages{messages: records},
	}
	ctx := types.WithExecutionTenant(context.Background(), 1)
	reads := map[string]func() ([]*types.Message, error){
		"page":   func() ([]*types.Message, error) { return s.GetMessagesBySession(ctx, "session", 1, 30) },
		"recent": func() ([]*types.Message, error) { return s.GetRecentMessagesBySession(ctx, "session", 30) },
		"before": func() ([]*types.Message, error) {
			return s.GetMessagesBySessionBeforeTime(ctx, "session", time.Now(), 30)
		},
	}
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			got, err := read()
			require.NoError(t, err)
			require.Len(t, got, 3)
			require.Zero(t, got[0].AgentSourceTenantID)
			require.Equal(t, uint64(84), got[1].AgentSourceTenantID)
			require.Zero(t, got[2].AgentSourceTenantID)
			raw, err := json.Marshal(got)
			require.NoError(t, err)
			var history []map[string]any
			require.NoError(t, json.Unmarshal(raw, &history))
			require.NotContains(t, history[0], "agent_source_tenant_id")
			require.Equal(t, float64(84), history[1]["agent_source_tenant_id"])
			require.NotContains(t, history[2], "agent_source_tenant_id")
			require.NotContains(t, history[1], "agent_tenant_id", "internal execution binding stays private")
		})
	}
	got, err := s.GetMessage(ctx, "session", "shared")
	require.NoError(t, err)
	require.Equal(t, uint64(84), got.AgentSourceTenantID)
	require.Zero(t, records[1].AgentSourceTenantID, "history projection must not mutate repository records")
	require.Equal(t, uint64(84), records[1].AgentTenantID)
}

func TestForkedMessageKeepsBorrowedAgentSource(t *testing.T) {
	original := &types.Message{
		ID: "source-answer", SessionID: "source", Role: "assistant",
		AgentID: types.BuiltinSmartReasoningID, AgentTenantID: 84,
	}
	copies := copyMessagesInto("fork", []*types.Message{original})
	require.Len(t, copies, 1)
	require.Equal(t, uint64(84), copies[0].AgentTenantID)
	require.Equal(t, "fork", copies[0].SessionID)
	projected := projectMessageAgentSources(copies, 1)
	require.Equal(t, uint64(84), projected[0].AgentSourceTenantID)
	require.Zero(t, original.AgentSourceTenantID)
	// Reading in the execution workspace itself must never select a share.
	require.Zero(t, projectMessageAgentSources(copies, 84)[0].AgentSourceTenantID)
}

func TestMessageHistoryUsesAuthorizedSessionOwnerForBorrowedSource(t *testing.T) {
	records := []*types.Message{
		{ID: "own", SessionID: "session", Role: "assistant", AgentID: types.BuiltinSmartReasoningID, AgentTenantID: 1},
		{ID: "shared", SessionID: "session", Role: "assistant", AgentID: types.BuiltinSmartReasoningID, AgentTenantID: 84},
	}
	s := &messageService{
		sessionRepo: &agentSourceHistorySessions{ownerTenantID: 1},
		messageRepo: &agentSourceHistoryMessages{messages: records},
	}
	ctx := types.WithExecutionTenant(context.Background(), 84)
	reads := map[string]func() ([]*types.Message, error){
		"page":   func() ([]*types.Message, error) { return s.GetMessagesBySession(ctx, "session", 1, 30) },
		"recent": func() ([]*types.Message, error) { return s.GetRecentMessagesBySession(ctx, "session", 30) },
		"before": func() ([]*types.Message, error) {
			return s.GetMessagesBySessionBeforeTime(ctx, "session", time.Now(), 30)
		},
	}
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			got, err := read()
			require.NoError(t, err)
			require.Zero(t, got[0].AgentSourceTenantID)
			require.Equal(t, uint64(84), got[1].AgentSourceTenantID)
		})
	}
	got, err := s.GetMessage(ctx, "session", "shared")
	require.NoError(t, err)
	require.Equal(t, uint64(84), got.AgentSourceTenantID)
	require.Equal(t, uint64(1), messageSessionTenantID(&types.Session{TenantID: 1}, 84))
	require.Equal(t, uint64(84), messageSessionTenantID(nil, 84))
	require.Equal(t, uint64(84), messageSessionTenantID(&types.Session{}, 84))
}

func TestForkPreservesBorrowedAgentContinuationSource(t *testing.T) {
	turn := checkpointedTurn("user", "answer", "sandbox", "commit", 0)
	turn[1].IsCompleted = true
	turn[1].AgentID = types.BuiltinSmartReasoningID
	turn[1].AgentTenantID = 84
	port := &fakeForkSandboxPort{boundID: "sandbox", bound: true, snapshotID: "snapshot"}
	svc, sessions, _ := newForkFixture(t, port, turn)
	sessions.source.LastRequestState = &types.SessionLastRequestState{
		AgentID: types.BuiltinSmartReasoningID, AgentSourceTenantID: 84, AgentEnabled: true,
	}
	_, err := svc.Fork(context.Background(), 1, "u1", "src", "answer", "")
	require.NoError(t, err)
	require.Equal(t, uint64(84), sessions.created.LastRequestState.AgentSourceTenantID)
	require.Len(t, sessions.copiedMessages, 2)
	require.Equal(t, uint64(84), sessions.copiedMessages[1].AgentTenantID)
	projected := projectMessageAgentSources(sessions.copiedMessages, sessions.created.TenantID)
	require.Equal(t, uint64(84), projected[1].AgentSourceTenantID)
}
