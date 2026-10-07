package service

import (
	"encoding/json"
	"github.com/Tencent/WeKnora/internal/types"
)

func messageSessionTenantID(session *types.Session, lookupTenantID uint64) uint64 {
	if session != nil && session.TenantID != 0 {
		return session.TenantID
	}
	return lookupTenantID
}

// projectMessageAgentSources runs only after the session read has been
// authorized. The internal execution tenant includes own agents too; only a
// different workspace is an explicit source selector for continuation. Copies
// keep the API projection out of repository objects and persisted history.
func projectMessageAgentSources(messages []*types.Message, sessionTenantID uint64) []*types.Message {
	if messages == nil {
		return nil
	}
	out := make([]*types.Message, len(messages))
	for i, message := range messages {
		if message == nil {
			continue
		}
		copy := *message
		copy.AgentSourceTenantID = 0
		copy.DocumentFormatting = nil
		if copy.Role == "assistant" && copy.ExecutionContext.TenderFormatting != nil {
			if info := copy.ExecutionContext.TenderFormatting.Info; info != nil {
				data, _ := json.Marshal(info)
				var public types.DocumentFormattingInfo
				if json.Unmarshal(data, &public) == nil {
					copy.DocumentFormatting = &public
				}
			}
		}
		if copy.Role == "assistant" && copy.AgentID != "" && sessionTenantID != 0 &&
			copy.AgentTenantID != 0 && copy.AgentTenantID != sessionTenantID {
			copy.AgentSourceTenantID = copy.AgentTenantID
		}
		out[i] = &copy
	}
	return out
}
