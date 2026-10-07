import type { SessionLastRequestStatePayload } from '@/stores/settings'

type Message = Record<string, any>

export type AnswerContinuationState = SessionLastRequestStatePayload

/** Continuation is a new turn, so only the transcript's current answer is eligible. */
export function continuableAnswerId(
  messages: Message[],
  opts: { embeddedMode?: boolean; outgoingWork?: boolean } = {},
): string {
  if (opts.embeddedMode || opts.outgoingWork) return ''
  if (messages.some(message => message.role === 'assistant' && !message.is_completed)) return ''
  const last = messages.at(-1)
  if (!last || last.role !== 'assistant' || !last.is_completed || last.persistence_error
      || last.steerForked || !String(last.content || '').trim()) return ''
  return String(last.id || '')
}

export function continuationUserMessage(messages: Message[], answerId: string): Message | undefined {
  const index = messages.findIndex(message => String(message.id || '') === answerId)
  if (index < 0) return undefined
  const answer = messages[index]!
  const preceding = messages.slice(0, index).reverse()
  return preceding.find(message => message.role === 'user' && answer.request_id
    && message.request_id === answer.request_id)
    || preceding.find(message => message.role === 'user')
}

/** Live messages already carry the POST snapshot; GET replay snapshots have no body. */
export function messageContinuationState(message: Message, sessionId: string): AnswerContinuationState | null {
  const request = message.debugRequest
  if (request?.method !== 'POST' || request.sessionId !== sessionId || !request.body) return null
  const body = request.body
  return { ...body, model_id: body.summary_model_id || '' }
}

export function resolveAnswerContinuationState(
  message: Message,
  state: AnswerContinuationState | null | undefined,
  userMessage?: Message,
): AnswerContinuationState | null {
  const agentId = String(message.agent_id || state?.agent_id || '')
  if (!agentId && typeof state?.agent_enabled !== 'boolean') return null
  const agentEnabled = agentId === 'builtin-quick-answer' ? false
    : agentId === 'builtin-smart-reasoning' ? true : state?.agent_enabled
  if (typeof agentEnabled !== 'boolean') return null
  const mentionedItems = [...(state?.mentioned_items || [])]
  for (const item of userMessage?.mentioned_items || []) {
    if (!mentionedItems.some(existing => existing.type === item.type && existing.id === item.id)) {
      mentionedItems.push({ ...item })
    }
  }
  // Missing/omitted scope is intentionally empty: the original agent may use
  // all KBs. Retrieval references cannot reconstruct the requested scope.
  return {
    agent_id: agentId,
    agent_enabled: agentEnabled,
    agent_source_tenant_id: message.agent_source_tenant_id || state?.agent_source_tenant_id,
    model_id: String(message.model_id || state?.model_id || ''),
    reasoning_effort: state?.reasoning_effort,
    knowledge_base_ids: [...(state?.knowledge_base_ids || [])],
    knowledge_ids: [...(state?.knowledge_ids || [])],
    tag_ids: [...(state?.tag_ids || [])],
    mcp_service_ids: [...(state?.mcp_service_ids || [])],
    skill_names: [...(state?.skill_names || [])],
    mentioned_items: mentionedItems,
    web_search_enabled: state?.web_search_enabled === true,
    local_browser_enabled: state?.local_browser_enabled === true,
  }
}

/** Reuse parsed attachments in the same session; do not upload the source again. */
export function continuationAttachments(userMessage?: Message): Message[] {
  return (userMessage?.attachments || []).filter((attachment: Message) => attachment.id)
    .map((attachment: Message) => ({
      documentId: attachment.id, status: 'ready',
      name: attachment.file_name || 'attachment', size: attachment.file_size || 0,
    }))
}
