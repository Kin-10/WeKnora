import type { BidOpportunity, BidSearchPage, BidSearchRequest, BidUnit } from '../../api/bid-opportunities'

export const MAX_SEARCH_PAGES = 100
export const BID_TYPES = ['招标公告', '中标结果', '采购意向', '审批项目']
export const TIME_OPTIONS = [
  { label: '不限时间', value: 0 }, { label: '近 3 天', value: 1 },
  { label: '近 7 天', value: 2 }, { label: '近 1 个月', value: 3 },
  { label: '近 3 个月', value: 8 }, { label: '近 6 个月', value: 4 },
  { label: '近 1 年', value: 5 }, { label: '近 3 年', value: 9 },
]
export type BidSearchFilters = {
  keyword: string; bidTypes: string[]; province: string; city: string; county: string
  timeInterval: number; dateRange: string[]; matchType: 'term' | 'fuzzy'
  orderBy: BidSearchRequest['orderBy']
}
export function defaultBidFilters(): BidSearchFilters {
  return { keyword: '', bidTypes: [], province: '', city: '', county: '', timeInterval: 0,
    dateRange: [], matchType: 'term', orderBy: 'PUBLISH_DATE_DESC' }
}
export function splitRegions(value: string): string[] {
  return [...new Set(value.split(/[,，、;；\n]+/).map(part => part.trim()).filter(Boolean))]
}
export function buildBidSearchRequest(filters: BidSearchFilters, pageNum = 1, time = ''): BidSearchRequest {
  const dates = filters.dateRange.length === 2 && filters.dateRange.every(value => /^\d{4}-\d{2}-\d{2}$/.test(value))
    ? filters.dateRange : []
  return { keyword: filters.keyword.trim(), bidTypes: [...filters.bidTypes],
    provinces: splitRegions(filters.province), cities: splitRegions(filters.city), counties: splitRegions(filters.county),
    matchType: filters.matchType, orderBy: filters.orderBy, pageNum, pageSize: 20, time,
    timeInterval: dates.length ? 0 : filters.timeInterval,
    startTime: dates[0] ? `${dates[0]} 00:00:00` : '', endTime: dates[1] ? `${dates[1]} 23:59:59` : '' }
}

/** Search HTML is reduced to text and always rendered with Vue interpolation. */
export function bidText(value: unknown): string {
  if (typeof value !== 'string') return ''
  const named: Record<string, string> = { amp: '&', lt: '<', gt: '>', quot: '"', apos: "'", nbsp: ' ' }
  return value.replace(/<(script|style)\b[^>]*>[\s\S]*?<\/\1\s*>/gi, '')
    .replace(/<!--[\s\S]*?-->/g, '').replace(/<(?:br\b[^>]*|\/p|\/div|\/li)>/gi, '\n')
    .replace(/<[^>]*>/g, '')
    .replace(/&(#x[\da-f]+|#\d+|amp|lt|gt|quot|apos|nbsp);/gi, (match, entity: string) => {
      if (!entity.startsWith('#')) return named[entity.toLowerCase()] ?? match
      const n = entity[1]?.toLowerCase() === 'x' ? parseInt(entity.slice(2), 16) : parseInt(entity.slice(1), 10)
      return Number.isInteger(n) && n > 0 && n <= 0x10ffff && !(n >= 0xd800 && n <= 0xdfff)
        ? String.fromCodePoint(n) : ''
    }).replace(/[\t\r ]+/g, ' ').replace(/\n\s*\n+/g, '\n').trim()
}
export function unitNames(value?: BidUnit | null): string {
  const names = [...new Set((value?.units ?? []).map(unit => bidText(unit.name) || bidText(unit.nameAlias)).filter(Boolean))]
  return names.length ? names.join('、') : bidText(value?.name)
}
export function bidLocation(item: BidOpportunity): string {
  return [...new Set([item.province, item.city, item.region].map(bidText).filter(Boolean))].join(' · ')
}
export function sourceLink(value: unknown): string {
  if (typeof value !== 'string' || /[\u0000-\u0020\u007f]/.test(value) || !/^https?:\/\//i.test(value)) return ''
  try {
    const url = new URL(value)
    return url.hostname && !url.username && !url.password && ['http:', 'https:'].includes(url.protocol) ? url.href : ''
  } catch { return '' }
}
export function opportunityKey(item: BidOpportunity): string {
  return item.id || item.md5Id || JSON.stringify([item.title, item.publishDate, item.province, item.city, item.mainBody])
}
export function searchError(error: unknown): { message: string; traceId: string } {
  const candidate = error as { message?: unknown; error?: { message?: unknown; details?: { trace_id?: unknown } }; details?: { trace_id?: unknown } }
  return { message: bidText(candidate?.error?.message || candidate?.message) || '搜索暂时不可用，请稍后重试。',
    traceId: bidText(candidate?.error?.details?.trace_id || candidate?.details?.trace_id) }
}

type StoredPage = { items: BidOpportunity[]; cursor: string }
type PageSearch = (request: BidSearchRequest) => Promise<BidSearchPage>

/** Cached sequential pages preserve each upstream cursor without inventing a total. */
export class BidSearchPager {
  pages = new Map<number, StoredPage>()
  seen = new Set<string>()
  currentPage = 0
  total: number | null = null
  loading = false
  searched = false
  exhausted = false
  stopReason: 'empty' | 'duplicates' | 'total' | 'limit' | null = null
  error: { message: string; traceId: string } | null = null
  generation = 0
  query: BidSearchRequest | null = null
  failedRequest: { page: number; cursor: string } | null = null

  constructor(readonly fetchPage: PageSearch) {}
  get items(): BidOpportunity[] { return this.pages.get(this.currentPage)?.items ?? [] }
  get loadedCount(): number { return this.seen.size }
  get hasPrevious(): boolean { return this.currentPage > 1 }
  get hasNext(): boolean {
    return this.currentPage > 0 && (this.pages.has(this.currentPage + 1)
      || (!this.exhausted && this.currentPage < MAX_SEARCH_PAGES))
  }
  get hitLimit(): boolean { return this.pages.size >= MAX_SEARCH_PAGES }

  reset(): void {
    this.generation++
    this.pages.clear(); this.seen.clear(); this.currentPage = 0; this.total = null
    this.loading = false; this.searched = false; this.exhausted = false; this.error = null
    this.stopReason = null
    this.query = null; this.failedRequest = null
  }
  async search(filters: BidSearchFilters): Promise<void> {
    this.reset(); this.searched = true; this.query = buildBidSearchRequest(filters)
    await this.load(1, '')
  }
  previous(): void {
    if (this.loading || !this.hasPrevious) return
    this.currentPage--; this.error = null; this.failedRequest = null
  }
  async next(): Promise<void> {
    if (this.loading || !this.hasNext) return
    const page = this.currentPage + 1
    if (this.pages.has(page)) { this.currentPage = page; this.error = null; this.failedRequest = null; return }
    await this.load(page, this.pages.get(this.currentPage)?.cursor ?? '')
  }
  async retry(): Promise<void> {
    if (!this.loading && this.failedRequest) await this.load(this.failedRequest.page, this.failedRequest.cursor)
  }
  async load(page: number, cursor: string): Promise<void> {
    if (!this.query || page > MAX_SEARCH_PAGES) return
    const generation = ++this.generation
    this.loading = true; this.error = null; this.failedRequest = null
    try {
      const response = await this.fetchPage({ ...this.query, pageNum: page, time: cursor })
      if (generation !== this.generation) return
      const raw = response.items ?? []
      const items = raw.filter(item => {
        const key = opportunityKey(item)
        if (this.seen.has(key)) return false
        this.seen.add(key); return true
      })
      this.total = typeof response.total === 'number' && response.total >= 0 ? response.total : null
      this.stopReason = !raw.length ? 'empty' : !items.length ? 'duplicates' : page >= MAX_SEARCH_PAGES ? 'limit'
        : this.total !== null && this.seen.size >= this.total ? 'total' : null
      this.exhausted = this.stopReason !== null
      if (page === 1 || items.length) {
        this.pages.set(page, { items, cursor: response.next_time ?? '' }); this.currentPage = page
      }
    } catch (error) {
      if (generation !== this.generation) return
      this.error = searchError(error); this.failedRequest = { page, cursor }
    } finally {
      if (generation === this.generation) this.loading = false
    }
  }
}
