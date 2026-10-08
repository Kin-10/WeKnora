import type { BidGenerationTask } from '@/api/bid-generation'
import { persistedAssistantId } from '../../utils/steerStreamFork'

/** Only an explicit request to produce the whole document starts background writing. */
export function isFullBidGenerationQuery(value: unknown): boolean {
  if (typeof value !== 'string') return false
  const query = value.trim()
  if (!query) return false
  const clauses = query.split(/[，,；;\n。.!！]+/).map(clause => clause.trim()).filter(Boolean)
  // A later cancellation of the whole request supersedes an earlier command.
  const cancelled = clauses.some(clause =>
    /^(?:请|先|现在|暂时|但|但是|不过|还是|那)*(?:取消|停止|暂停|撤销)(?:本次|这次|当前|此次|整个)?(?:完整标书|标书|投标文件|生成|编写|制作|任务|请求){0,3}$/.test(clause)
    || /^(?:请|先|现在|暂时|但|但是|不过|还是|那)*(?:不要|不用|不需要|无需|别|暂不|不)(?:再|继续|现在|立即|自动|直接|帮我|为我|给我|替我|你|让你)*(?:生成|编写|制作|撰写|输出)(?:(?:完整|整份|全部|全篇)?(?:标书|投标文件))?$/.test(clause)
    || /^(?:please\s+)?(?:cancel|stop|pause)(?:\s+(?:generation|this request|this task))?$/i.test(clause))
  if (cancelled) return false
  return clauses.some(clause => {
    const command = clause.match(/(?:生成|编写|制作|撰写|输出).{0,16}(?:完整|整份|全部|全篇).{0,10}(?:标书|投标文件)/)
      || clause.match(/(?:完整|整份|全部|全篇).{0,10}(?:标书|投标文件).{0,16}(?:生成|编写|制作|撰写|输出)/)
      || clause.match(/\b(?:generate|write|create)\s+(?:a\s+|the\s+)?(?:complete|full)\s+(?:bid|tender)(?:\s+document)?\b/i)
    if (!command) return false
    // Questions about generation stay ordinary chat; questions in a different
    // clause and constraints such as “不要虚构资质” do not negate the command.
    if (/[?？]|(?:是否|能否|能不能|是不是|怎么|如何|为什么|还是)|(?:吗|么)$/.test(clause)
        || /\b(?:how|why|whether|should|can i)\b/i.test(clause)) return false
    const beforeEnd = clause.slice(0, (command.index || 0) + command[0].length)
    if (/(?:不要|不用|不需要|不想|无需|别|不能|暂不|不)(?:再|继续|现在|立即|自动|直接|帮我|为我|给我|替我|你|让你)*(?:生成|编写|制作|撰写|输出)/.test(beforeEnd)
        || /(?:取消|停止|暂停|撤销)(?:生成|编写|制作|撰写|输出)/.test(beforeEnd)
        || /\b(?:do not|don't|cannot|can't|not yet)\s+(?:want to\s+)?(?:generate|write|create)\b/i.test(beforeEnd)) return false
    const prefix = clause.slice(0, command.index || 0)
    const suffix = clause.slice((command.index || 0) + command[0].length)
    return !/^(?:请|帮我|给我|我想|我希望|不要|别)*(?:讨论|了解|研究|分析|评估|解释|说明|例如|比如|上次|之前|我之前|我上次)/.test(prefix)
      && !/^(?:的)?(?:方法|方案|流程|步骤|原理|建议|策略|思路|示例)/.test(suffix)
  })
}

export function isBidOutlineConfirmation(value: unknown): boolean {
  return typeof value === 'string' && /^(?:确认(?:目录|大纲)?|目录已确认|大纲已确认|按(?:这个|此|该)(?:目录|大纲)(?:开始|继续)?(?:生成|编写|写作)?|开始(?:生成|编写)|继续生成|可以|好的|ok)[。.!！\s]*$/i.test(value.trim())
}

export function bidGenerationComposerControl(task: BidGenerationTask | null | undefined, value: unknown): 'resume' | 'cancel' | null {
  if (!task || typeof value !== 'string' || ['completed', 'cancelled'].includes(task.status)) return null
  const query = value.trim().replace(/[。.!！]+$/, '')
  if (/^(?:取消(?:本次|当前)?(?:标书)?(?:生成|任务)|停止生成|cancel(?: generation| this task)?)$/i.test(query)) return 'cancel'
  if (['paused', 'failed'].includes(task.status)
      && /^(?:继续(?:自动)?(?:生成|编写)(?:完整)?(?:标书)?|恢复生成|重试|重新尝试|resume(?: generation)?|retry)$/i.test(query)) return 'resume'
  return null
}

export function bidGenerationIsWriting(task: BidGenerationTask | null | undefined): boolean {
  return !!task && (task.status === 'planning' || task.status === 'running')
}
export function bidGenerationIsWaiting(task: BidGenerationTask | null | undefined): boolean {
  return !!task && (task.status === 'awaiting_input' || task.status === 'awaiting_outline')
}
export function bidGenerationIsActive(task: BidGenerationTask | null | undefined): boolean {
  return !!task && !['completed', 'cancelled', 'failed'].includes(task.status)
}
export function bidGenerationLocksComposer(task: BidGenerationTask | null | undefined): boolean {
  return bidGenerationIsWriting(task)
}

/** A card from an earlier section cannot supply answers to the current checkpoint. */
export function bidGenerationAcceptsMessage(task: BidGenerationTask | null | undefined, messageId: string): boolean {
  return bidGenerationIsWaiting(task) && !!messageId && task?.pending_message_id === messageId
}

/** Keep the current task's input below its chapter list, using the saved checkpoint identity. */
export function bidGenerationPendingInputMessage(
  task: BidGenerationTask | null | undefined, messages: Record<string, any>[],
): Record<string, any> | null {
  if (!task?.pending_message_id || ['completed', 'cancelled'].includes(task.status)) return null
  return messages.find(message => message.role === 'assistant' && !message.steerForked
    && persistedAssistantId(message) === task.pending_message_id) || null
}

/** The job's final message summarizes the document; its text is not another full draft. */
export function hasCompleteBidDocument(message: Record<string, any> | null | undefined): boolean {
  return Array.isArray(message?.artifacts) && message.artifacts.some((artifact: Record<string, any>) =>
    /(?:^|\/)generated-documents\/bid-task\//.test(String(artifact?.source_path || '').replace(/\\/g, '/')))
}

/** Preserve reactive message identity when the worker updates a persisted section. */
export function mergeBidGenerationMessages(
  existing: Record<string, any>[], incoming: Record<string, any>[],
): Record<string, any>[] {
  const byId = new Map(existing.filter(message => message.id).map(message => [String(message.id), message]))
  const additions: Record<string, any>[] = []
  for (const message of incoming) {
    const id = String(message.id || '')
    if (!id) continue
    const current = byId.get(id)
    if (current) {
      Object.assign(current, message)
    } else {
      additions.push(message)
      byId.set(id, message)
    }
  }
  return additions
}
