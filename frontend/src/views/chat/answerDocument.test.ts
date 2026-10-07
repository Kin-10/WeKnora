import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import test from 'node:test'
import { reactive, ref } from 'vue'
import { collectSessionArtifacts } from '../../utils/sessionArtifacts.ts'
import { persistedAssistantId } from '../../utils/steerStreamFork.ts'
import { canGenerateAnswerDocument, updateAnswerDocumentArtifacts } from './answerDocument.ts'
import { readDocumentFormatting, documentFormattingSuccess } from '../../utils/documentFormatting.ts'

test('only completed, persisted text answers offer document generation', () => {
  const answer = { id: 'answer', role: 'assistant', content: '# 标书', is_completed: true }
  assert.equal(canGenerateAnswerDocument(answer), true)
  for (const changed of [{ id: '' }, { role: 'user' }, { is_completed: false },
    { content: ' ' }, { persistence_error: 'not saved' }, { steerForked: true }]) {
    assert.equal(canGenerateAnswerDocument({ ...answer, ...changed }), false)
  }
  assert.equal(canGenerateAnswerDocument(answer, { embeddedMode: true }), false)
  assert.equal(canGenerateAnswerDocument(answer, { outgoingWork: true }), false)
})

test('document metadata preserves deleted artifact indices and other generated files', () => {
  const answer: Record<string, any> = { id: 'answer', role: 'assistant', artifacts: [
    { file_name: 'old.txt', deleted_at: 'deleted' }, { file_name: 'chart.png', index: 1 },
  ] }
  updateAnswerDocumentArtifacts(answer, [{ index: 3, file_name: '标书.docx' } as any])
  const files = collectSessionArtifacts([answer])
  assert.deepEqual(files.map(file => [file.index, file.file_name]), [[1, 'chart.png'], [3, '标书.docx']])
  assert.equal(answer.artifacts[2].deleted_at.length > 0, true)
  updateAnswerDocumentArtifacts(answer, [{ index: 3, file_name: '标书.docx', file_size: 2500 } as any])
  assert.equal(collectSessionArtifacts([answer]).length, 2)
  assert.equal(answer.artifacts[3].file_size, 2500)
  updateAnswerDocumentArtifacts(answer, [{ index: 3, file_name: '标书.docx', file_size: 2500 } as any], true)
  assert.deepEqual(collectSessionArtifacts([answer]).map(file => file.index), [3])
  assert.equal(answer.artifacts[1].file_name, 'chart.png')
  assert.equal(!!answer.artifacts[1].deleted_at, true)
})

// Execute the production parent handler with transport/download boundaries mocked.
const source = readFileSync(new URL('./index.vue', import.meta.url), 'utf8')
const handler = source.slice(source.indexOf('const documentExportInFlight ='), source.indexOf('const continuationInFlight ='))
function deferred() {
  let resolve!: (value: any) => void
  const promise = new Promise<any>(yes => { resolve = yes })
  return { promise, resolve }
}
function harness(overrides: Record<string, any> = {}) {
  const generation: any[] = [], downloads: any[] = [], deliveries: any[] = [], errors: string[] = [], successes: string[] = []
  const answer = { id: 'visual-segment', assistant_message_id: 'persisted-answer', role: 'assistant',
    content: '# 原标书\n\n正文内容', is_completed: true }
  const state = {
    ref, canGenerateAnswerDocument, updateAnswerDocumentArtifacts, persistedAssistantId,
    readDocumentFormatting, documentFormattingSuccess,
    composerLocked: ref(false), forkInFlight: false, isReplying: ref(false), isStreaming: ref(false),
    isImRecovering: ref(false), props: { embeddedMode: false }, session_id: ref('original-session'),
    messagesList: reactive([answer]),
    generateMessageDocument: async (...args: any[]) => {
      generation.push(args)
      return { success: true, data: { index: 0, message_id: 'persisted-answer', file_name: '标书.docx' } }
    },
    downloadArtifact: async (...args: any[]) => { downloads.push(args); return new Blob(['DOCX']) },
    listMessageArtifacts: async () => ({ success: true, data: [{ index: 0, file_name: '标书.docx', file_size: 4 }] }),
    downloadAnswerDocument: (...args: any[]) => deliveries.push(args),
    MessagePlugin: { error: (message: string) => errors.push(message), success: (message: string) => successes.push(message) },
    t: (key: string, values?: Record<string, string>) => values?.sources ? `${key}: ${values.sources}` : key,
    ...overrides,
  }
  const actions = vm.runInNewContext(`${handler}\n({ handleGenerateDocument, canGenerateDocument, documentExportInFlight })`, state)
  return { ...actions, state, generation, downloads, deliveries, errors, successes }
}

test('generating Word uses persisted ownership, downloads through artifacts, and keeps the answer intact', async () => {
  const h = harness()
  await h.handleGenerateDocument('visual-segment')
  assert.deepEqual(h.generation, [['original-session', 'persisted-answer']])
  assert.deepEqual(h.downloads, [['original-session', 'persisted-answer', 0]])
  assert.equal(h.deliveries[0][1], '标书.docx')
  assert.equal(h.state.messagesList[0].content, '# 原标书\n\n正文内容')
  assert.equal(h.state.messagesList[0].id, 'visual-segment')
  assert.equal(h.state.messagesList[0].artifacts[0].file_size, 4)
  assert.equal(h.documentExportInFlight.value, null)
  assert.deepEqual(h.successes, [], 'old servers without formatting metadata keep their original interaction')
})

test('manual tender export shows its source and warning without changing the answer', async () => {
  const formatting = { mode: 'tender', scope: 'technical', source_files: ['本次招标.pdf'],
    summary: ['宋体四号', '固定行距 30 磅'], warning: '附件需核对' }
  const h = harness({ generateMessageDocument: async () => ({ success: true,
    data: { index: 0, message_id: 'persisted-answer', file_name: '技术标.docx', formatting } }) })
  await h.handleGenerateDocument('visual-segment')
  assert.deepEqual(h.state.messagesList[0].document_formatting, formatting)
  assert.equal(h.state.messagesList[0].content, '# 原标书\n\n正文内容')
  assert.deepEqual(h.successes, ['chat.wordGeneratedFromTender: 本次招标.pdf'])
  assert.equal(h.deliveries.length, 1)
})

test('blocked manual export shows the backend reason and keeps the text available', async () => {
  const formatting = { mode: 'blocked', scope: 'business', source_files: ['本次招标.pdf'],
    summary: [], warning: '请分别生成商务标与技术暗标。' }
  const h = harness({ generateMessageDocument: async () => { throw {
    status: 409, data: { formatting }, message: 'Document formatting unavailable',
  } } })
  await h.handleGenerateDocument('visual-segment')
  assert.deepEqual(h.state.messagesList[0].document_formatting, formatting)
  assert.deepEqual(h.errors, ['请分别生成商务标与技术暗标。'])
  assert.equal(h.state.messagesList[0].content, '# 原标书\n\n正文内容')
  assert.equal(h.deliveries.length, 0)
})

test('duplicate clicks while generation is pending create only one document', async () => {
  const pending = deferred()
  let calls = 0
  const h = harness({ generateMessageDocument: () => { calls++; return pending.promise } })
  const first = h.handleGenerateDocument('visual-segment')
  assert.equal(h.canGenerateDocument(h.state.messagesList[0]), false)
  await h.handleGenerateDocument('visual-segment')
  pending.resolve({ success: true, data: { index: 0, message_id: 'persisted-answer', file_name: '标书.docx' } })
  await first
  assert.equal(calls, 1)
  assert.equal(h.deliveries.length, 1)
})

for (const race of ['navigation', 'new-generation', 'message-removed']) {
  test(`${race} during generation cannot update or download into the new chat/run`, async () => {
    const pending = deferred()
    const h = harness({ generateMessageDocument: () => pending.promise })
    const run = h.handleGenerateDocument('visual-segment')
    if (race === 'navigation') h.state.session_id.value = 'other-session'
    else if (race === 'new-generation') h.state.isReplying.value = true
    else h.state.messagesList.splice(0)
    pending.resolve({ success: true, data: { index: 0, message_id: 'persisted-answer', file_name: '标书.docx' } })
    await run
    assert.equal(h.downloads.length, 0)
    assert.equal(h.deliveries.length, 0)
    assert.equal(h.errors.length, 0)
  })
}

test('navigation while artifact bytes download cannot trigger a late browser download', async () => {
  const pending = deferred()
  const h = harness({ downloadArtifact: () => pending.promise })
  const run = h.handleGenerateDocument('visual-segment')
  await Promise.resolve()
  h.state.session_id.value = 'other-session'
  pending.resolve(new Blob(['DOCX']))
  await run
  assert.equal(h.deliveries.length, 0)
  assert.equal(h.errors.length, 0)
})

test('a metadata refresh failure still downloads the already generated document', async () => {
  const h = harness({ listMessageArtifacts: async () => { throw new Error('offline') } })
  await h.handleGenerateDocument('visual-segment')
  assert.equal(h.deliveries.length, 1)
  assert.equal(h.state.messagesList[0].artifacts[0].file_name, '标书.docx')
  assert.equal(h.errors.length, 0)
})

test('a download failure retains generated artifact metadata for retry and preserves text', async () => {
  const h = harness({ downloadArtifact: async () => { throw new Error('offline') } })
  await h.handleGenerateDocument('visual-segment')
  assert.equal(h.deliveries.length, 0)
  assert.equal(h.state.messagesList[0].artifacts[0].file_name, '标书.docx')
  assert.equal(h.state.messagesList[0].content, '# 原标书\n\n正文内容')
  assert.deepEqual(h.errors, ['chat.generateWordFailed'])
})

test('a malformed document response cannot download another message artifact', async () => {
  const h = harness({ generateMessageDocument: async () => ({ success: true,
    data: { index: 0, message_id: 'another-message', file_name: 'other.docx' } }) })
  await h.handleGenerateDocument('visual-segment')
  assert.equal(h.downloads.length, 0)
  assert.deepEqual(h.errors, ['chat.generateWordFailed'])
})
