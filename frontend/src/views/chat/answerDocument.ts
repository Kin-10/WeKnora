import type { ArtifactMeta } from '@/api/chat'
import { persistedAssistantId } from '../../utils/steerStreamFork'

type Message = Record<string, any>

/** Document generation never exports an unfinished or unsaved answer. */
export function canGenerateAnswerDocument(
  message: Message | undefined,
  opts: { embeddedMode?: boolean; outgoingWork?: boolean } = {},
): boolean {
  return !opts.embeddedMode && !opts.outgoingWork && !!message
    && message.role === 'assistant' && message.is_completed === true
    && !message.persistence_error && !message.steerForked
    && !!persistedAssistantId(message) && !!String(message.content || '').trim()
}

/** Keep per-message indices stable when a list response omits deleted files. */
export function updateAnswerDocumentArtifacts(
  message: Message,
  incoming: ArtifactMeta[],
  replaceLive = false,
): void {
  const existing = Array.isArray(message.artifacts) ? message.artifacts : []
  const byIndex = new Map<number, Record<string, any>>()
  existing.forEach((item: Record<string, any>, index: number) => {
    if (item) byIndex.set(Number.isInteger(item.index) ? item.index : index, {
      index, ...item,
      ...(replaceLive && !item.deleted_at ? { deleted_at: new Date().toISOString() } : {}),
    })
  })
  for (const item of incoming) {
    if (Number.isInteger(item.index) && item.index >= 0) byIndex.set(item.index, { ...item })
  }
  if (!byIndex.size) return
  const last = Math.max(...byIndex.keys())
  message.artifacts = Array.from({ length: last + 1 }, (_, index) => byIndex.get(index) || {
    index, deleted_at: new Date().toISOString(),
  })
}

export function downloadAnswerDocument(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename || 'document.docx'
  document.body.appendChild(link)
  try { link.click() } finally {
    link.remove()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  }
}
