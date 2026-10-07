import type { DocumentFormatting } from '@/api/chat'

/** Formatting metadata is separate from the answer; never rewrite its body. */
export function readDocumentFormatting(value: unknown): DocumentFormatting | undefined {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined
  const data = value as Record<string, unknown>
  if (data.mode !== 'tender' && data.mode !== 'default' && data.mode !== 'blocked') return undefined
  if (data.scope !== 'business' && data.scope !== 'technical') return undefined
  const strings = (input: unknown) => Array.isArray(input)
    ? input.filter((item): item is string => typeof item === 'string' && !!item.trim()) : []
  return {
    mode: data.mode as DocumentFormatting['mode'], scope: data.scope,
    source_files: strings(data.source_files), summary: strings(data.summary),
    ...(typeof data.warning === 'string' && data.warning.trim() ? { warning: data.warning } : {}),
  }
}

export function documentFormattingLabel(
  formatting: DocumentFormatting,
  t: (key: string, values?: Record<string, string>) => string,
): string {
  if (formatting.mode === 'blocked') return t('chat.wordFormattingBlocked')
  if (formatting.mode === 'default') return t('chat.wordFormattingDefault')
  return t('chat.wordFormattingTender', {
    sources: formatting.source_files.join('、') || t('chat.wordFormattingTenderSource'),
  })
}

export function documentFormattingSuccess(
  formatting: DocumentFormatting,
  t: (key: string, values?: Record<string, string>) => string,
): string {
  return formatting.mode === 'tender'
    ? t('chat.wordGeneratedFromTender', {
      sources: formatting.source_files.join('、') || t('chat.wordFormattingTenderSource'),
    })
    : t('chat.wordGeneratedDefault')
}

export function documentFormattingSourceLabel(
  formatting: DocumentFormatting,
  t: (key: string, values?: Record<string, string>) => string,
): string {
  return formatting.source_files.length
    ? t('chat.wordFormattingCheckedSources', { sources: formatting.source_files.join('、') })
    : ''
}
