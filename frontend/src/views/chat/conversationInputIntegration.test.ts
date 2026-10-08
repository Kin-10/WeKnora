import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import test from 'node:test'
import { reactive, ref } from 'vue'
import { conversationInputReply, conversationInputRequests, canSubmitConversationInput } from '../../utils/conversationInput.ts'
import { continuationUserMessage, continuationAttachments, messageContinuationState, resolveAnswerContinuationState } from './answerContinuation.ts'
import { persistedAssistantId } from '../../utils/steerStreamFork.ts'
import { bidGenerationIsWaiting, bidGenerationAcceptsMessage } from './bidGeneration.ts'

const source = readFileSync(new URL('./index.vue', import.meta.url), 'utf8')
const handler = source.slice(source.indexOf('const conversationInputInFlight ='), source.indexOf('function stashForkLanding'))
const request = { id: 'scope', title: '编写范围', questions: [
  { id: 'scope', label: '编写哪部分', type: 'single' as const, required: true,
    options: [{ value: 'business', label: '商务标' }] },
] }
const card = '```weknora-input\n' + JSON.stringify(request) + '\n```'

function deferred() {
  let resolve!: (value: any) => void
  return { promise: new Promise<any>(yes => { resolve = yes }), resolve: (value: any) => resolve(value) }
}
function harness(overrides: Record<string, any> = {}) {
  const sent: any[] = [], errors: string[] = []
  const state = {
    ref, conversationInputRequests, conversationInputReply, canSubmitConversationInput,
    persistedAssistantId, bidGenerationIsWaiting, bidGenerationAcceptsMessage, bidGenerationTask: ref(null),
    continuationUserMessage, continuationAttachments, messageContinuationState, resolveAnswerContinuationState,
    session_id: ref('session'), documentExportInFlight: ref(null), continuationBlocked: () => false,
    messagesList: reactive([
      { id: 'user', role: 'user', request_id: 'request', mentioned_items: [{ type: 'file', id: 'tender' }],
        attachments: [{ id: 'attachment', file_name: '本次招标.pdf', file_size: 200 }] },
      { id: 'answer', role: 'assistant', request_id: 'request', is_completed: true, answerFullyRendered: true, content: card,
        agent_id: 'bid-agent', model_id: 'bid-model' },
    ]),
    getSession: async () => ({ data: { last_request_state: {
      agent_id: 'bid-agent', agent_enabled: true, model_id: 'bid-model',
      knowledge_base_ids: ['history-kb'], knowledge_ids: ['tender'], web_search_enabled: false,
    } } }),
    sendMsg: async (...args: any[]) => { sent.push(args) },
    t: (key: string) => key, MessagePlugin: { error: (message: string) => errors.push(message), warning: (message: string) => errors.push(message) },
    ...overrides,
  }
  const actions = vm.runInNewContext(`${handler}\n({ handleConversationInputSubmit, canSubmitInputFor })`, state)
  return { ...actions, state, sent, errors }
}

test('choice submission is a normal new turn with original agent, retrieval scope and tender attachments', async () => {
  const h = harness()
  const answer = h.state.messagesList.at(-1)
  await h.handleConversationInputSubmit(answer, request, { scope: 'business' })
  assert.equal(h.sent.length, 1)
  const [reply, model, mentions, images, attachments, options] = h.sent[0]
  assert.match(reply, /编写哪部分：商务标/)
  assert.equal(model, 'bid-model')
  assert.equal(mentions[0].id, 'tender')
  assert.deepEqual(Array.from(images), [])
  assert.equal(attachments[0].documentId, 'attachment')
  assert.equal(options.requestState.agent_id, 'bid-agent')
  assert.equal(options.requestState.agent_enabled, true)
  assert.deepEqual(Array.from(options.requestState.knowledge_base_ids), ['history-kb'])
  assert.deepEqual(Array.from(options.requestState.knowledge_ids), ['tender'])
  assert.equal(options.continuationOfMessageId, undefined, 'facts start a new turn; they do not extend a draft Word chain')
  assert.equal(answer.content, card)
})

test('double submit while original settings load sends once', async () => {
  const pending = deferred()
  const h = harness({ getSession: () => pending.promise })
  const answer = h.state.messagesList.at(-1)
  const first = h.handleConversationInputSubmit(answer, request, { scope: 'business' })
  assert.equal(h.canSubmitInputFor(answer), false)
  await h.handleConversationInputSubmit(answer, request, { scope: 'business' })
  pending.resolve({ data: { last_request_state: { agent_enabled: true } } })
  await first
  assert.equal(h.sent.length, 1)
})

for (const interrupt of ['navigation', 'later-message', 'busy']) {
  test(`${interrupt} during settings read cannot submit stale input`, async () => {
    const pending = deferred()
    const h = harness({ getSession: () => pending.promise })
    const first = h.handleConversationInputSubmit(h.state.messagesList.at(-1), request, { scope: 'business' })
    if (interrupt === 'navigation') h.state.session_id.value = 'other-session'
    if (interrupt === 'later-message') h.state.messagesList.push({ role: 'user', content: 'new query' })
    if (interrupt === 'busy') h.state.documentExportInFlight.value = { messageId: 'other' }
    pending.resolve({ data: { last_request_state: { agent_enabled: true } } })
    await first
    assert.equal(h.sent.length, 0)
  })
}

test('invalid required fields and failed state reads preserve card without sending', async () => {
  const h = harness()
  await h.handleConversationInputSubmit(h.state.messagesList.at(-1), request, {})
  assert.equal(h.sent.length, 0)
  assert.equal(h.errors.length, 1)
  const failed = harness({ getSession: async () => { throw Error('offline') } })
  const answer = failed.state.messagesList.at(-1)
  await failed.handleConversationInputSubmit(answer, request, { scope: 'business' })
  assert.equal(failed.sent.length, 0)
  assert.equal(failed.canSubmitInputFor(answer), true)
  assert.equal(answer.content, card)
  assert.equal(failed.errors.length, 1)
})

test('a bid checkpoint card sends facts back to the same task without rebuilding the original request state', async () => {
  const h = harness({ bidGenerationTask: ref({ id: 'task', status: 'awaiting_input', pending_message_id: 'answer' }),
    getSession: async () => { throw new Error('must not reload original scope') } })
  await h.handleConversationInputSubmit(h.state.messagesList.at(-1), request, { scope: 'business' })
  assert.equal(h.sent.length, 1)
  assert.match(h.sent[0][0], /商务标/)
  assert.equal(h.sent[0].length, 1, 'composer dispatch uses the existing task scope')
  assert.equal(h.errors.length, 0)
})

test('older or paused bid checkpoint cards cannot answer a different current checkpoint', () => {
  const h = harness({ bidGenerationTask: ref({ id: 'task', status: 'awaiting_input', pending_message_id: 'different-answer' }) })
  assert.equal(h.canSubmitInputFor(h.state.messagesList.at(-1)), false)
  h.state.bidGenerationTask.value = { id: 'task', status: 'paused', pending_message_id: 'answer' }
  assert.equal(h.canSubmitInputFor(h.state.messagesList.at(-1)), false)
})
