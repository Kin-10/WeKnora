import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import test from 'node:test'
import { computed, reactive, ref } from 'vue'
import { persistedAssistantId } from '../../utils/steerStreamFork.ts'
import { isFullBidGenerationQuery, bidGenerationIsWaiting, bidGenerationAcceptsMessage, bidGenerationComposerControl } from './bidGeneration.ts'
import {
  continuableAnswerId, continuationUserMessage, messageContinuationState,
  resolveAnswerContinuationState, continuationAttachments,
} from './answerContinuation.ts'

test('only the current completed text answer can continue, including legacy answers without a truncation flag', () => {
  const answer = { id: 'answer', role: 'assistant', content: 'old answer', is_completed: true }
  assert.equal(continuableAnswerId([answer]), 'answer')
  for (const changed of [{ is_completed: false }, { persistence_error: 'not saved' },
    { content: ' ' }, { steerForked: true }, { role: 'user' }]) {
    assert.equal(continuableAnswerId([{ ...answer, ...changed }]), '')
  }
  assert.equal(continuableAnswerId([answer, { id: 'new-user', role: 'user' }]), '')
  assert.equal(continuableAnswerId([answer], { outgoingWork: true }), '')
  assert.equal(continuableAnswerId([answer], { embeddedMode: true }), '')
})

test('source agent/model override session defaults, and retrieval references never narrow an omitted original scope', () => {
  const state = resolveAnswerContinuationState({
    agent_id: 'builtin-quick-answer', model_id: 'original-model',
    knowledge_references: [{ knowledge_id: 'retrieved-file', knowledge_base_id: 'retrieved-kb' }],
  }, { agent_id: 'other-agent', agent_enabled: true, model_id: 'other-model' })!
  assert.equal(state.agent_id, 'builtin-quick-answer')
  assert.equal(state.agent_enabled, false)
  assert.equal(state.model_id, 'original-model')
  assert.deepEqual(state.knowledge_base_ids, [])
  assert.deepEqual(state.knowledge_ids, [])
  assert.equal(resolveAnswerContinuationState({}, undefined), null)
})

test('the complete bid job final summary is not offered as an unfinished document continuation', () => {
  assert.equal(continuableAnswerId([{ id: 'summary', role: 'assistant', content: '完整标书已生成。', is_completed: true,
    artifacts: [{ source_path: 'generated-documents/bid-task/task-id/完整标书.docx' }] }]), '')
  assert.equal(continuableAnswerId([{ id: 'draft', role: 'assistant', content: '草稿正文', is_completed: true,
    artifacts: [{ source_path: 'generated-documents/draft-message/草稿.docx' }] }]), 'draft')
})

test('only a POST snapshot belonging to this session supplies original settings', () => {
  const snapshot = { method: 'POST', sessionId: 'session', body: {
    agent_id: 'builtin-quick-answer', agent_enabled: false, summary_model_id: 'original-model',
    agent_source_tenant_id: 7,
  } }
  assert.equal(messageContinuationState({ debugRequest: snapshot }, 'session')?.model_id, 'original-model')
  assert.equal(messageContinuationState({ debugRequest: snapshot }, 'session')?.agent_source_tenant_id, 7)
  assert.equal(messageContinuationState({ debugRequest: snapshot }, 'other-session'), null)
  assert.equal(messageContinuationState({ debugRequest: { ...snapshot, method: 'GET', body: null } }, 'session'), null)
})

test('explicit mentions and parsed attachments come from the corresponding user turn', () => {
  const user = { role: 'user', request_id: 'original-request', mentioned_items: [
    { type: 'file', id: 'original-file', name: 'original.docx' },
  ], attachments: [{ id: 'parsed-document', file_name: 'original.docx', file_size: 200 }] }
  const answer = { id: 'answer', role: 'assistant', request_id: 'original-request' }
  const source = continuationUserMessage([user, answer], 'answer')!
  assert.equal(source, user)
  assert.equal(resolveAnswerContinuationState({ agent_id: 'builtin-quick-answer' }, {}, source)
    ?.mentioned_items?.[0]?.id, 'original-file')
  assert.deepEqual(continuationAttachments(source), [{
    documentId: 'parsed-document', status: 'ready', name: 'original.docx', size: 200,
  }])
})

// Execute the production entry point and sendMsg together. Transport/session
// dependencies are mocked; scope selection, guards and the POST args are real.
const source = readFileSync(new URL('./index.vue', import.meta.url), 'utf8')
const handlers = source.slice(source.indexOf('const continuationInFlight ='), source.indexOf('function stashForkLanding'))
const send = source.slice(source.indexOf('const sendMsg ='), source.indexOf('// Quietly recover an in-flight IM reply'))
function deferred() {
  let resolve!: (value: any) => void
  const promise = new Promise<any>(yes => { resolve = yes })
  return { promise, resolve }
}
function harness(overrides: Record<string, any> = {}) {
  const answer = { id: 'answer', assistant_message_id: 'answer', role: 'assistant',
    request_id: 'request', content: 'original partial answer', is_completed: true,
    agent_id: 'builtin-quick-answer', model_id: 'original-model' }
  const requests: any[] = [], errors: string[] = []
  let stopped = 0, fetched = 0
  const state = {
    ref, computed, continuableAnswerId, continuationUserMessage, messageContinuationState,
    isFullBidGenerationQuery, bidGenerationIsWaiting, bidGenerationAcceptsMessage, bidGenerationComposerControl,
    bidGenerationTask: ref(null), bidGenerationRequestBusy: ref(false), bidTaskEpoch: 1,
    resolveAnswerContinuationState, continuationAttachments, persistedAssistantId,
    session_id: ref('session'), props: { embeddedMode: false }, composerLocked: ref(false), forkInFlight: false,
    isReplying: ref(false), isStreaming: ref(false), isImRecovering: ref(false), loading: ref(false),
    activitySessionId: ref('session'), activeStreamAgentMode: ref(null), userHasScrolledUp: ref(false),
    messagesList: reactive([{ role: 'user', request_id: 'request', content: 'write a long answer' }, answer]),
    useSettingsStoreInstance: { isAgentStreamMode: true, selectedAgentId: 'builtin-smart-reasoning',
      selectedAgentSourceTenantId: 'new-tenant', reasoningEffortOverride: 'max',
      isWebSearchEnabled: true, isLocalBrowserEnabled: true,
      settings: { selectedKnowledgeBases: ['new-kb'], selectedFiles: ['new-file'] } },
    useBrowserConnectionStore: () => ({ knownOffline: false }),
    pendingSuggestionKnowledgeBaseIds: ['unrelated-suggestion-kb'], pendingSuggestionAttribution: { question_id: 'unrelated' },
    stopStream: () => { stopped++ }, prepareForNewOutgoingMessage() {}, scrollToBottom() {},
    startStream: async (request: any) => { requests.push(request) },
    getSession: async () => { fetched++; return { data: { last_request_state: {
      agent_id: 'builtin-quick-answer', agent_enabled: false, model_id: 'original-model',
      knowledge_base_ids: ['original-kb'], knowledge_ids: ['original-file'], tag_ids: ['original-tag'],
      web_search_enabled: false, local_browser_enabled: false,
    } } } },
    MessagePlugin: { error: (message: string) => errors.push(message) }, t: (key: string) => key,
    ...overrides,
  }
  const actions = vm.runInNewContext(`${handlers}\n${send}\n({ handleContinueAnswer, continuableMessageId })`, state)
  return { ...actions, state, requests, errors, stopped: () => stopped, fetched: () => fetched }
}

test('continuation uses the original quick mode/model/scope after selectors change and preserves the old answer', async () => {
  const h = harness()
  await h.handleContinueAnswer('answer')
  assert.equal(h.requests.length, 1)
  const request = h.requests[0]
  assert.equal(request.method, 'POST')
  assert.equal(request.url, '/api/v1/knowledge-chat')
  assert.equal(request.agent_id, 'builtin-quick-answer')
  assert.equal(request.agent_enabled, false)
  assert.equal(request.summary_model_id, 'original-model')
  assert.equal(request.continuation_of_message_id, 'answer')
  assert.deepEqual([...request.knowledge_base_ids], ['original-kb'])
  assert.deepEqual([...request.knowledge_ids], ['original-file'])
  assert.deepEqual([...request.tag_ids], ['original-tag'])
  assert.equal(request.web_search_enabled, false)
  assert.equal(request.local_browser_enabled, false)
  assert.equal(request.reasoning_effort, undefined)
  assert.equal(request.agent_source_tenant_id, undefined)
  assert.equal(request.suggestion_attribution, undefined)
  assert.equal(h.state.activeStreamAgentMode.value, false)
  assert.equal(h.state.messagesList[1].id, 'answer')
  assert.equal(h.state.messagesList[1].content, 'original partial answer')
})

test('omitted original KB/file scope remains empty and invokes the same agent rather than UI selections', async () => {
  const h = harness({ getSession: async () => ({ data: { last_request_state: {
    agent_id: 'builtin-quick-answer', agent_enabled: false,
  } } }) })
  await h.handleContinueAnswer('answer')
  assert.deepEqual([...h.requests[0].knowledge_base_ids], [])
  assert.deepEqual([...h.requests[0].knowledge_ids], [])
})

for (const origin of ['live', 'reloaded'] as const) {
  test(`${origin}: the answer's shared builtin source overrides stale request/session source`, async () => {
    const h = harness()
    const answer = h.state.messagesList[1]
    answer.agent_source_tenant_id = 7
    if (origin === 'live') {
      answer.debugRequest = { method: 'POST', sessionId: 'session', body: {
        agent_id: 'builtin-quick-answer', agent_enabled: false, agent_source_tenant_id: 9,
      } }
    } else {
      const originalGet = h.state.getSession
      h.state.getSession = async () => {
        const res = await originalGet()
        res.data.last_request_state.agent_source_tenant_id = 9
        return res
      }
    }
    await h.handleContinueAnswer('answer')
    assert.equal(h.requests[0].agent_source_tenant_id, 7)
    assert.equal(h.requests[0].agent_id, 'builtin-quick-answer')
    assert.equal(h.requests[0].agent_enabled, false)
    assert.equal(h.fetched(), origin === 'live' ? 0 : 1)
  })
}

test('a legacy answer without shared source retains the session request source', async () => {
  const h = harness()
  const originalGet = h.state.getSession
  h.state.getSession = async () => {
    const res = await originalGet()
    res.data.last_request_state.agent_source_tenant_id = 7
    return res
  }
  await h.handleContinueAnswer('answer')
  assert.equal(h.requests[0].agent_source_tenant_id, 7)
})

test('duplicate clicks while settings load create only one new turn', async () => {
  const pending = deferred()
  const h = harness({ getSession: () => pending.promise })
  const first = h.handleContinueAnswer('answer')
  await h.handleContinueAnswer('answer')
  pending.resolve({ data: { last_request_state: { agent_enabled: false } } })
  await first
  assert.equal(h.requests.length, 1)
  assert.equal(h.stopped(), 1)
})

for (const race of ['navigation', 'new-generation'] as const) {
  test(`${race} while original settings load cannot send into or abort the new chat/run`, async () => {
    const pending = deferred()
    const h = harness({ getSession: () => pending.promise })
    const first = h.handleContinueAnswer('answer')
    if (race === 'navigation') h.state.session_id.value = 'other-session'
    else h.state.isReplying.value = true
    pending.resolve({ data: { last_request_state: { agent_enabled: false } } })
    await first
    assert.equal(h.requests.length, 0)
    assert.equal(h.stopped(), 0)
    assert.equal(h.errors.length, 0)
  })
}

test('a settings read failure preserves the old answer and does not start generation', async () => {
  const h = harness({ getSession: async () => { throw new Error('offline') } })
  await h.handleContinueAnswer('answer')
  assert.equal(h.requests.length, 0)
  assert.equal(h.stopped(), 0)
  assert.equal(h.state.messagesList[1].content, 'original partial answer')
  assert.deepEqual(h.errors, ['chat.continueAnswerFailed'])
})
