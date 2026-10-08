import assert from 'node:assert/strict'
import test from 'node:test'
import {
  validateConversationInput, conversationInputRequests, conversationInputMarkdown,
  conversationInputReply, canSubmitConversationInput,
  conversationInputProblem,
} from './conversationInput.ts'
import { continuableAnswerId } from '../views/chat/answerContinuation.ts'
import { canGenerateAnswerDocument } from '../views/chat/answerDocument.ts'

const request = { id: 'bid_scope', title: '确认编写范围', questions: [
  { id: 'scope', label: '编写哪部分', type: 'single', required: true,
    options: [{ value: 'business', label: '商务标' }, { value: 'technical', label: '技术标' }] },
  { id: 'company', label: '投标企业名称', type: 'text', required: true },
  { id: 'notes', label: '补充说明', type: 'text', required: false },
] }
const block = (value: unknown = request) => '```weknora-input\n' + JSON.stringify(value) + '\n```'

test('a persisted request round-trips while prose remains visible', () => {
  const answer = '我先确认两个信息。\n\n' + block() + '\n'
  assert.deepEqual(conversationInputRequests(answer), [request])
  assert.equal(conversationInputMarkdown(answer), '我先确认两个信息。')
  assert.equal(conversationInputRequests(JSON.parse(JSON.stringify({ content: answer })).content).length, 1)
})

test('streaming and malformed completion hide protocol data with a readable fallback state', () => {
  for (const tail of ['```weknora-input', '```weknora-input\n{"id":', block().slice(0, -1)]) {
    assert.equal(conversationInputMarkdown('请补充。\n\n' + tail, true), '请补充。')
    assert.deepEqual(conversationInputRequests(tail), [])
  }
  const malformed = '```weknora-input\nnot json\n```'
  assert.equal(conversationInputMarkdown(malformed), '')
  assert.equal(conversationInputProblem(malformed), true)
  assert.deepEqual(conversationInputRequests(malformed), [])
  assert.equal(conversationInputMarkdown('```json\n{"hello":1}', true), '```json\n{"hello":1}')
})

test('ordinary fenced examples and code with matching protocol text are never interactive', () => {
  const example = '````markdown\n' + block() + '\n````'
  assert.deepEqual(conversationInputRequests(example), [])
  assert.equal(conversationInputMarkdown(example), example)
  assert.deepEqual(conversationInputRequests('    ' + block().replaceAll('\n', '\n    ')), [])
  assert.equal(conversationInputRequests(block().replaceAll('```', '~~~')).length, 1)
})

test('reject oversized, duplicate, invalid types and unsafe identities rather than partially rendering', () => {
  for (const invalid of [
    { ...request, id: '<script>' }, { ...request, title: 'x'.repeat(161) },
    { ...request, questions: Array(7).fill(request.questions[0]) },
    { ...request, questions: [request.questions[0], request.questions[0]] },
    { ...request, questions: [{ ...request.questions[0], required: 'true' }] },
    { ...request, questions: [{ ...request.questions[0], type: 'file' }] },
    { ...request, questions: [{ ...request.questions[0], options: [] }] },
    { ...request, questions: [{ ...request.questions[0], options: Array(13).fill({ value: 'x', label: 'x' }) }] },
    { ...request, questions: [{ ...request.questions[0], options: [{ value: 'x', label: 'a' }, { value: 'x', label: 'b' }] }] },
  ]) {
    assert.equal(validateConversationInput(invalid), null)
    assert.equal(conversationInputMarkdown(block(invalid)), '')
    assert.equal(conversationInputProblem(block(invalid)), true)
  }
})

test('choice labels, custom answers and optional fields become readable chat context', () => {
  const parsed = validateConversationInput(request)!
  assert.equal(conversationInputReply(parsed, { scope: ['business', '补充说明：独立成册'], company: '示例企业' },
    '补充信息：确认编写范围', '请继续'),
  '补充信息：确认编写范围\n\n编写哪部分：商务标；补充说明：独立成册\n投标企业名称：示例企业\n\n请继续')
  assert.equal(conversationInputReply(parsed, { scope: 'business' }, 'intro', 'continue'), null)
  assert.equal(conversationInputReply(parsed, { scope: '用户自定义范围', company: '公司' }, 'intro', 'continue')?.includes('用户自定义范围'), true)
  assert.equal(conversationInputReply(parsed, { scope: 'business', company: 'x'.repeat(5001) }, 'intro', 'continue'), null)
  const optional = { ...parsed, questions: parsed.questions.map(question => ({ ...question, required: false })) }
  assert.equal(conversationInputReply(optional, {}, 'intro', 'continue'), 'intro\n\ncontinue')
})

test('duplicate protocol blocks never leak JSON while only one card is active', () => {
  assert.equal(conversationInputRequests(block() + '\n' + block()).length, 1)
  assert.equal(conversationInputMarkdown('请确认。\n' + block() + '\n' + block()), '请确认。')
})

test('only a current completed saved card can submit; awaiting facts cannot continue or export', () => {
  const answer = { id: 'answer', role: 'assistant', is_completed: true, content: block() }
  assert.equal(canSubmitConversationInput(answer, [answer]), true)
  assert.equal(canSubmitConversationInput(answer, [answer], true), false)
  assert.equal(canSubmitConversationInput(answer, [answer, { role: 'user' }]), false)
  for (const patch of [{ is_completed: false }, { id: '' }, { persistence_error: 'save failed' }, { steerForked: true }]) {
    const changed = { ...answer, ...patch }
    assert.equal(canSubmitConversationInput(changed, [changed]), false)
  }
  assert.equal(continuableAnswerId([answer]), '')
  assert.equal(canGenerateAnswerDocument(answer), false)
  assert.equal(continuableAnswerId([{ ...answer, content: '# 标书正文' }]), 'answer')
  assert.equal(canGenerateAnswerDocument({ ...answer, content: '# 标书正文' }), true)
})
