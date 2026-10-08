export type ConversationInputQuestion = {
  id: string
  label: string
  type: 'single' | 'multiple' | 'text'
  required: boolean
  options?: { value: string; label: string }[]
  placeholder?: string
}

export type ConversationInputRequest = {
  id: string
  title: string
  questions: ConversationInputQuestion[]
}

export type ConversationInputValues = Record<string, string | string[]>
type Block = { start: number; end: number; request: ConversationInputRequest }

const record = (value: unknown): value is Record<string, unknown> =>
  !!value && typeof value === 'object' && !Array.isArray(value)
const text = (value: unknown, max: number): value is string =>
  typeof value === 'string' && !!value.trim() && value.length <= max
const identifier = (value: unknown): value is string =>
  typeof value === 'string' && /^[a-zA-Z0-9_-]{1,64}$/.test(value)

/** Treat model output as untrusted data; only a complete valid request becomes a form. */
export function validateConversationInput(value: unknown): ConversationInputRequest | null {
  if (!record(value) || !identifier(value.id) || !text(value.title, 160)
      || !Array.isArray(value.questions) || !value.questions.length || value.questions.length > 6) return null
  const questionIds = new Set<string>()
  const questions: ConversationInputQuestion[] = []
  for (const question of value.questions) {
    if (!record(question) || !identifier(question.id) || questionIds.has(question.id)
        || !text(question.label, 500) || !['single', 'multiple', 'text'].includes(String(question.type))
        || (question.required !== undefined && typeof question.required !== 'boolean')
        || (question.placeholder !== undefined && !text(question.placeholder, 300))) return null
    questionIds.add(question.id)
    const type = question.type as ConversationInputQuestion['type']
    const options: { value: string; label: string }[] = []
    if (type !== 'text') {
      if (!Array.isArray(question.options) || !question.options.length || question.options.length > 12) return null
      const values = new Set<string>()
      for (const option of question.options) {
        if (!record(option) || !text(option.value, 200) || !text(option.label, 300)
            || values.has(option.value)) return null
        values.add(option.value)
        options.push({ value: option.value, label: option.label })
      }
    } else if (question.options !== undefined) return null
    questions.push({ id: question.id, label: question.label.trim(), type,
      required: question.required !== false,
      ...(options.length ? { options } : {}),
      ...(question.placeholder ? { placeholder: String(question.placeholder).trim() } : {}),
    })
  }
  return { id: value.id, title: value.title.trim(), questions }
}

/** Only recognize top-level protocol fences, never examples nested inside other fences. */
function inputBlocks(content: string): { blocks: Block[]; spans: { start: number; end: number }[]; incompleteStart: number } {
  const blocks: Block[] = []
  const spans: { start: number; end: number }[] = []
  let fence: { char: string; length: number; start: number; bodyStart: number; input: boolean } | null = null
  let offset = 0
  for (const line of content.match(/[^\n]*\n|[^\n]+$/g) || []) {
    const trimmedLine = line.replace(/\r?\n$/, '')
    if (!fence) {
      const opening = trimmedLine.match(/^ {0,3}(`{3,}|~{3,})([^\n]*)$/)
      if (opening) fence = { char: opening[1]![0]!, length: opening[1]!.length,
        start: offset, bodyStart: offset + line.length, input: opening[2]!.trim() === 'weknora-input' }
    } else {
      const closing = trimmedLine.match(/^ {0,3}(`{3,}|~{3,})\s*$/)
      if (closing && closing[1]![0] === fence.char && closing[1]!.length >= fence.length) {
        const body = content.slice(fence.bodyStart, offset)
        if (fence.input) spans.push({ start: fence.start, end: offset + line.length })
        if (fence.input && body.length <= 32768 && blocks.length < 1) {
          try {
            const request = validateConversationInput(JSON.parse(body))
            if (request) blocks.push({ start: fence.start, end: offset + line.length, request })
          } catch { /* The chat UI offers a readable retry notice instead of exposing JSON. */ }
        }
        fence = null
      }
    }
    offset += line.length
  }
  return { blocks, spans, incompleteStart: fence?.input ? fence.start : -1 }
}

export function conversationInputRequests(content: unknown): ConversationInputRequest[] {
  return typeof content === 'string' ? inputBlocks(content).blocks.map(block => block.request) : []
}

/** Hide protocol text during streaming without mutating the persisted answer. */
export function conversationInputMarkdown(content: unknown, _streaming = false): string {
  if (typeof content !== 'string') return ''
  const { spans, incompleteStart } = inputBlocks(content)
  const end = incompleteStart >= 0 ? incompleteStart : content.length
  let result = '', offset = 0
  for (const block of spans) {
    result += content.slice(offset, block.start)
    offset = block.end
  }
  result += content.slice(offset, end)
  return result.trimEnd()
}

export function conversationInputProblem(content: unknown): boolean {
  if (typeof content !== 'string') return false
  const { blocks, spans, incompleteStart } = inputBlocks(content)
  return !blocks.length && (spans.length > 0 || incompleteStart >= 0)
}

/** Submission uses the existing chat transport and persists readable answers. */
export function conversationInputReply(
  request: ConversationInputRequest,
  values: ConversationInputValues,
  intro: string,
  continuation: string,
): string | null {
  const lines: string[] = []
  for (const question of request.questions) {
    const raw = values[question.id]
    const answers = (Array.isArray(raw) ? raw : typeof raw === 'string' ? [raw] : [])
      .map(value => value.trim()).filter(Boolean)
    if (question.required && !answers.length) return null
    if (!answers.length) continue
    if (answers.some(value => value.length > 5000) || answers.length > 13) return null
    const labels = answers.map(value => question.options?.find(option => option.value === value)?.label || value)
    lines.push(`${question.label}：${labels.join('；')}`)
  }
  return `${intro}${lines.length ? `\n\n${lines.join('\n')}` : ''}\n\n${continuation}`
}

/** A historic or unsaved card cannot send into a newer conversation turn. */
export function canSubmitConversationInput(message: Record<string, any>, messages: Record<string, any>[], blocked = false): boolean {
  return !blocked && message === messages.at(-1) && message.role === 'assistant'
    && message.is_completed === true && !!message.id && !message.persistence_error && !message.steerForked
    && conversationInputRequests(message.content).length > 0
}
