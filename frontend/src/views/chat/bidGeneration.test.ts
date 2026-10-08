import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'
import { ref, reactive, computed, createRenderer, nextTick, watch } from 'vue'
import { useTypewriter } from '../../composables/useTypewriter'
import { conversationInputMarkdown } from '../../utils/conversationInput'
import type { BidGenerationTask } from '@/api/bid-generation'
import {
  isFullBidGenerationQuery, isBidOutlineConfirmation, bidGenerationIsActive,
  bidGenerationIsWaiting, bidGenerationIsWriting, bidGenerationLocksComposer,
  bidGenerationAcceptsMessage, mergeBidGenerationMessages,
  bidGenerationComposerControl,
  bidGenerationPendingInputMessage,
} from './bidGeneration'

const task = (status: BidGenerationTask['status'] = 'running'): BidGenerationTask => ({
  id: 'task-a', session_id: 'session-a', status, revision: 7, phase: 'writing', title: '测试标书',
  sections: [{ id: 'technical', title: '技术方案', status: 'writing', word_count: 0 }],
  current_section: 0, total_words: 0, pending_message_id: 'checkpoint-a',
})
test('only explicit whole-document requests start automatic generation', () => {
  for (const query of ['请生成完整标书', '帮我编写一份完整的投标文件', '把完整标书生成出来', 'Generate a complete bid document']) {
    assert.equal(isFullBidGenerationQuery(query), true, query)
  }
  for (const query of ['生成一个章节', '完整标书怎么生成', '一次性生成完整标书合适吗', '不要生成完整标书', '暂不生成完整标书', '生成完整标书？', 'do not generate a complete bid document', 'I cannot generate a complete bid document', 'how to generate a complete bid document']) {
    assert.equal(isFullBidGenerationQuery(query), false, query)
  }
})
test('additional constraints and separate questions do not negate an explicit generation command', () => {
  for (const query of ['请生成完整标书，不要虚构企业资质', '请生成完整标书，交付期限是多少？',
    '请生成完整标书，不需要虚构企业业绩。', '请生成完整标书不要虚构企业资质',
    '请生成完整标书，取消正文中的多余占位符', 'Generate a complete bid document, do not invent qualifications']) {
    assert.equal(isFullBidGenerationQuery(query), true, query)
  }
  for (const query of ['不要生成完整标书', '如何生成完整标书', '完整标书是否一次生成',
    '请给出生成完整标书的方案', '讨论生成完整标书的流程', '请生成完整标书，不过取消本次生成请求。',
    '请帮我讨论生成完整标书', '我想了解生成完整标书', '不需要你生成完整标书',
    '请生成完整标书，但先不要生成', 'Generate a complete bid document, cancel this task',
    "I don't want to generate a complete bid document"]) {
    assert.equal(isFullBidGenerationQuery(query), false, query)
  }
})
test('outline confirmation is exact and revisions or objections do not silently approve', () => {
  for (const query of ['确认目录', '按此目录开始生成', '好的。', 'OK']) assert.equal(isBidOutlineConfirmation(query), true)
  for (const query of ['确认目录前请修改第三章', '不要确认', '这个目录合适吗']) assert.equal(isBidOutlineConfirmation(query), false)
})
test('short conversation commands resume or cancel an existing task without consuming unrelated discussion', () => {
  assert.equal(bidGenerationComposerControl(task('paused'), '继续生成完整标书'), 'resume')
  assert.equal(bidGenerationComposerControl(task('failed'), '重试'), 'resume')
  assert.equal(bidGenerationComposerControl(task('awaiting_input'), '取消生成'), 'cancel')
  assert.equal(bidGenerationComposerControl(task('paused'), '继续生成会丢失数据吗'), null)
  assert.equal(bidGenerationComposerControl(task('completed'), '重试'), null)
})
test('writing locks the composer; waiting accepts only its persisted checkpoint; paused ordinary chat remains available', () => {
  assert.equal(bidGenerationIsWriting(task()), true)
  assert.equal(bidGenerationLocksComposer(task()), true)
  assert.equal(bidGenerationLocksComposer(task('paused')), false)
  assert.equal(bidGenerationIsActive(task('paused')), true)
  assert.equal(bidGenerationIsActive(task('completed')), false)
  assert.equal(bidGenerationIsWaiting(task('awaiting_input')), true)
  assert.equal(bidGenerationAcceptsMessage(task('awaiting_input'), 'checkpoint-a'), true)
  assert.equal(bidGenerationAcceptsMessage(task('awaiting_input'), 'older-checkpoint'), false)
  assert.equal(bidGenerationAcceptsMessage(task('completed'), 'checkpoint-a'), false)
})
test('chapter supplements use the current saved checkpoint after refresh and remain bound while paused', () => {
  const older = { id: 'older-checkpoint', role: 'assistant', content: '旧问题' }
  const current = { id: 'checkpoint-a', role: 'assistant', content: '本章补充卡片' }
  const messages = [older, { id: 'user', role: 'user' }, current]
  assert.equal(bidGenerationPendingInputMessage(task('awaiting_input'), messages), current)
  assert.equal(bidGenerationPendingInputMessage(task('paused'), messages), current)
  assert.equal(bidGenerationPendingInputMessage(task('completed'), messages), null)
  assert.equal(bidGenerationPendingInputMessage(task('cancelled'), messages), null)
  assert.equal(bidGenerationPendingInputMessage(task('awaiting_input'), [older]), null)
  assert.equal(bidGenerationPendingInputMessage(task('awaiting_input'), [{ ...current, steerForked: true }]), null)
  assert.equal(bidGenerationPendingInputMessage(null, messages), null)
})
test('worker history merges by persisted IDs and preserves existing reactive objects and local state', () => {
  const existing = reactive([{ id: 'section-a', role: 'assistant', content: '原内容', answerFullyRendered: true, document_formatting: { mode: 'tender' }, localFlag: true }])
  const original = existing[0]
  const additions = mergeBidGenerationMessages(existing, [
    { id: 'section-a', role: 'assistant', content: '完整内容', is_completed: true },
    { id: 'section-b', role: 'assistant', content: '下一章', is_completed: true, artifacts: [{ file_name: '标书.docx' }] },
    { id: 'section-b', role: 'assistant', content: '下一章完整版', is_completed: true },
  ])
  assert.equal(existing[0], original)
  assert.equal(existing[0]!.content, '完整内容')
  assert.equal(existing[0]!.answerFullyRendered, true, 'the renderer owns visual readiness')
  assert.equal(existing[0]!.localFlag, true)
  assert.equal(additions.length, 1)
  assert.equal(additions[0]!.content, '下一章完整版')
  assert.equal(additions[0]!.artifacts[0].file_name, '标书.docx')
})

test('a completed checkpoint update keeps its card visible when the renderer reconciles the answer immediately', async () => {
  const message = reactive({ id: 'checkpoint', role: 'assistant', content: '正在分析招标要求。', is_completed: true, answerFullyRendered: false })
  const renderer = createRenderer<Record<string, any>, Record<string, any>>({
    patchProp() {}, insert() {}, remove() {}, createElement: () => ({}),
    createText: () => ({}), createComment: () => ({}), setText() {}, setElementText() {},
    parentNode: () => null, nextSibling: () => null,
  })
  const app = renderer.createApp({ setup() {
      const visible = computed(() => conversationInputMarkdown(message.content))
      const { displayed } = useTypewriter(() => visible.value, () => message.is_completed)
      const ready = computed(() => message.is_completed && displayed.value.length >= visible.value.length)
      watch(ready, value => { message.answerFullyRendered = value }, { immediate: true })
      return () => null
    },
  })
  app.mount({})
  try {
    assert.equal(message.answerFullyRendered, true)
    const request = { id: 'company-details', title: '请补充企业信息', questions: [
      { id: 'company', label: '投标单位名称', type: 'text', required: true },
    ] }
    const content = `请先提供以下信息。\n\n\`\`\`weknora-input\n${JSON.stringify(request)}\n\`\`\``
    mergeBidGenerationMessages([message], [{ id: 'checkpoint', role: 'assistant', content, is_completed: true }])
    await nextTick()
    assert.equal(message.answerFullyRendered, true, 'a true-to-true renderer state must not be overwritten by history polling')
    const protocolOnlyChange = `${content}\n`
    mergeBidGenerationMessages([message], [{ id: 'checkpoint', role: 'assistant', content: protocolOnlyChange, is_completed: true }])
    await nextTick()
    assert.equal(message.answerFullyRendered, true)
  } finally { app.unmount() }
})

// Exercise the production composer dispatch, with only transport and UI boundaries mocked.
const source = readFileSync(new URL('./index.vue', import.meta.url), 'utf8')
const sendSource = source.slice(source.indexOf('const sendMsg = async'), source.indexOf('// Quietly recover'))
function harness(initial: BidGenerationTask | null = null, overrides: Record<string, any> = {}) {
  const starts: any[] = [], responses: any[] = [], streams: any[] = [], uploads: any[] = [], controls: string[] = [], errors: string[] = [], warnings: string[] = []
  const state: Record<string, any> = {
    composerLocked: ref(false), props: { embeddedMode: false },
    isFullBidGenerationQuery, isBidOutlineConfirmation, bidGenerationIsWaiting, bidGenerationComposerControl,
    bidGenerationTask: ref(initial), bidGenerationRequestBusy: ref(false), bidTaskEpoch: 1,
    session_id: ref('session-a'), activeStreamAgentMode: ref(null), activitySessionId: ref(''),
    isReplying: ref(false), loading: ref(false), messagesList: reactive([]), userHasScrolledUp: ref(false),
    useSettingsStoreInstance: { isAgentStreamMode: true, selectedAgentId: 'bid-agent', selectedAgentSourceTenantId: null,
      settings: { selectedKnowledgeBases: ['kb-a'], selectedFiles: ['file-a'] }, isWebSearchEnabled: false, isLocalBrowserEnabled: false },
    useBrowserConnectionStore: () => ({ knownOffline: false }),
    stopStream() {}, prepareForNewOutgoingMessage() {}, stopBidGenerationPolling() {}, scheduleBidGenerationPolling() {}, scrollToBottom() {},
    MessagePlugin: { error: (message: string) => errors.push(message), warning: (message: string) => warnings.push(message) },
    t: (key: string) => key, pendingSuggestionKnowledgeBaseIds: [], pendingSuggestionAttribution: null,
    fileToBase64: async () => 'data:image/png;base64,aA==',
    uploadTemporaryAttachment: async (...args: any[]) => { uploads.push(args); return { data: { id: 'attachment-a', status: 'pending' } } },
    deleteTemporaryAttachment: async () => {},
    startBidGeneration: async (...args: any[]) => { starts.push(args); return { success: true, data: task('planning') } },
    respondBidGeneration: async (...args: any[]) => { responses.push(args); return { success: true, data: task() } },
    startStream: async (...args: any[]) => streams.push(args),
    applyBidGenerationTask: async () => {}, loadBidGenerationState: async () => {}, bidGenerationActionError: (error: any) => errors.push(error.message),
    handleBidGenerationControl: async (action: string) => controls.push(action),
    usemenuStore: { updatasessionTitle() {} }, notifySessionMutation() {}, ...overrides,
  }
  const context = vm.createContext(state)
  const send = vm.runInContext(`${sendSource}\nsendMsg`, context)
  return { state: context, send, starts, responses, streams, uploads, controls, errors, warnings }
}
test('explicit full bid goes through job API after attachment upload with the original retrieval scope', async () => {
  const h = harness()
  await h.send('请生成完整标书', 'model-a', [{ type: 'kb', id: 'kb-mentioned' }], [], [{ file: {}, name: '招标文件.pdf', size: 20 }])
  assert.equal(h.uploads.length, 1)
  assert.equal(h.starts.length, 1)
  assert.equal(h.streams.length, 0)
  assert.equal(h.state.messagesList.length, 0, 'the worker owns persisted user messages')
  const request = h.starts[0][1]
  assert.equal(request.query, '请生成完整标书')
  assert.equal(request.request_state.model_id, 'model-a')
  assert.equal(request.request_state.agent_id, 'bid-agent')
  assert.equal(request.request_state.knowledge_base_ids.join(','), 'kb-a,kb-mentioned')
  assert.equal(request.attachment_ids.join(','), 'attachment-a')
  assert.equal(h.state.bidGenerationRequestBusy.value, false)
})
test('ordinary messages preserve the existing stream transport', async () => {
  const h = harness()
  await h.send('请解释这个产品参数', 'model-a')
  assert.equal(h.starts.length + h.responses.length, 0)
  assert.equal(h.streams.length, 1)
  assert.equal(h.streams[0][0].url, '/api/v1/agent-chat')
  assert.equal(h.state.messagesList.length, 1)
})
test('awaiting input responds to the same task with its revision and never starts another stream', async () => {
  const h = harness(task('awaiting_input'))
  await h.send('企业名称是测试公司', 'another-model')
  assert.equal(h.responses.length, 1)
  assert.equal(h.responses[0][0], 'session-a')
  assert.equal(h.responses[0][1], 'task-a')
  assert.equal(h.responses[0][2].expected_revision, 7)
  assert.equal(h.responses[0][2].confirm_outline, false)
  assert.equal(h.starts.length + h.streams.length, 0)
})
test('outline edits remain edits and an explicit approval starts chapter writing', async () => {
  const h = harness(task('awaiting_outline'))
  await h.send('增加售后服务章节')
  assert.equal(h.responses[0][2].confirm_outline, false)
  await h.send('确认目录')
  assert.equal(h.responses[1][2].confirm_outline, true)
  assert.equal(h.streams.length, 0)
})
test('failed or paused jobs prevent starting a duplicate full bid but permit ordinary chat', async () => {
  const h = harness(task('paused'))
  await h.send('生成完整标书')
  assert.equal(h.starts.length + h.streams.length, 0)
  assert.equal(h.warnings.length, 1)
  await h.send('请解释上一章的一句话')
  assert.equal(h.streams.length, 1)
})
test('conversation resume and cancel commands use task controls instead of a new LLM turn', async () => {
  const h = harness(task('paused'))
  await h.send('继续生成')
  await h.send('取消生成')
  assert.deepEqual(h.controls, ['resume', 'cancel'])
  assert.equal(h.starts.length + h.responses.length + h.streams.length, 0)
})
test('navigation during a deferred upload cannot start a job in a different chat or clear its new busy state', async () => {
  let finish!: (value: any) => void
  const pending = new Promise(resolve => { finish = resolve })
  const h = harness(null, { uploadTemporaryAttachment: () => pending })
  const sending = h.send('生成完整标书', '', [], [], [{ file: {}, name: '招标.pdf', size: 1 }])
  h.state.session_id.value = 'session-b'
  h.state.bidTaskEpoch++
  h.state.bidGenerationRequestBusy.value = true
  finish({ data: { id: 'attachment-a', status: 'pending' } })
  await sending
  assert.equal(h.starts.length + h.streams.length, 0)
  assert.equal(h.state.bidGenerationRequestBusy.value, true)
})
