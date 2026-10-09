import type {
  AnonymousBidCheckItem,
  AnonymousBidCheckReport,
  AnonymousBidCheckRequest,
  AnonymousBidCheckStatus,
} from '../../api/anonymous-bid-check'

export const MAX_CHECK_FILE_SIZE = 200 * 1024 * 1024
export const CHECK_FILE_ACCEPT = '.pdf,.doc,.docx,.txt,.md'
const SUPPORTED_EXTENSIONS = new Set(['pdf', 'doc', 'docx', 'txt', 'md'])

export function validateCheckFile(file: Pick<File, 'name' | 'size'>): 'empty' | 'format' | 'size' | null {
  if (file.size <= 0) return 'empty'
  const extension = file.name.split('.').pop()?.toLowerCase() || ''
  if (!SUPPORTED_EXTENSIONS.has(extension)) return 'format'
  if (file.size > MAX_CHECK_FILE_SIZE) return 'size'
  return null
}

export function parseIdentityKeywords(text: string): string[] {
  return [...new Set(text.split(/\r?\n/).map(value => value.trim()).filter(Boolean))]
}

export function validateIdentityKeywords(keywords: string[]): 'count' | 'length' | null {
  if (keywords.length > 50) return 'count'
  if (keywords.some(keyword => [...keyword].length < 2 || [...keyword].length > 100)) return 'length'
  return null
}

export function checkFileSize(size: number): string {
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`
  return `${(size / (1024 * 1024)).toFixed(1)} MB`
}

export function reportChecks(report: AnonymousBidCheckReport, filter: AnonymousBidCheckStatus | 'all') {
  return filter === 'all' ? report.checks : report.checks.filter(check => check.status === filter)
}

export function sortReportChecks(checks: readonly AnonymousBidCheckItem[]): AnonymousBidCheckItem[] {
  const priority: Record<AnonymousBidCheckStatus, number> = { fail: 0, review: 1, pass: 2 }
  return [...checks].sort((left, right) => priority[left.status] - priority[right.status])
}

/** A result belongs only to the files and options that started its request. */
export class AnonymousBidCheckRunner {
  loading = false
  report: AnonymousBidCheckReport | null = null
  error = ''
  private sequence = 0
  private controller: AbortController | null = null

  constructor(private readonly check: (request: AnonymousBidCheckRequest, signal: AbortSignal) => Promise<AnonymousBidCheckReport>) {}

  reset() {
    this.sequence++
    this.controller?.abort()
    this.controller = null
    this.loading = false
    this.report = null
    this.error = ''
  }

  async run(request: AnonymousBidCheckRequest, fallbackError: string) {
    this.reset()
    const sequence = this.sequence
    const controller = new AbortController()
    this.controller = controller
    this.loading = true
    try {
      const report = await this.check({ ...request, identityKeywords: [...request.identityKeywords] }, controller.signal)
      if (sequence === this.sequence) this.report = report
    } catch (error) {
      if (sequence === this.sequence && !controller.signal.aborted) {
        const message = (error as { message?: unknown } | null)?.message
        this.error = typeof message === 'string' && message ? message : fallbackError
      }
    } finally {
      if (sequence === this.sequence) {
        this.loading = false
        this.controller = null
      }
    }
  }
}
