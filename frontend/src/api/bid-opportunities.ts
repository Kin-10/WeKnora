import { post } from '@/utils/request'

export type BidSearchRequest = {
  keyword: string
  pageNum: number
  pageSize: 20
  matchType: 'term' | 'fuzzy'
  bidTypes: string[]
  provinces: string[]
  cities: string[]
  counties: string[]
  startTime: string
  endTime: string
  timeInterval: number
  orderBy: '' | 'PUBLISH_DATE_DESC' | 'RELEVANCE'
  time: string
}

export type BidUnit = {
  id?: string
  name?: string
  units?: { id?: string; name?: string; nameAlias?: string }[] | null
}

export type BidOpportunity = {
  id: string
  md5Id?: string
  title?: string
  publishDate?: string
  publishTime?: string
  province?: string
  city?: string
  region?: string | null
  bidType?: string
  subBidType?: string
  tenderProducts?: string
  mainBody?: string
  bidUnit?: BidUnit | null
  winBidUnit?: BidUnit | null
  budgetAmount?: string
  winBidAmount?: string
  bidEndDate?: string | null
  bidRemainingDays?: number | null
  time?: string
  source?: string
  tags?: { name?: string }[] | null
}

export type BidSearchPage = {
  items: BidOpportunity[]
  total: number | null
  page_num: number
  page_size: 20
  next_time: string
  trace_id: string
  upstream_code: number
}

export async function searchBidOpportunities(body: BidSearchRequest): Promise<BidSearchPage> {
  const response = await post<{ success: boolean; data: BidSearchPage }>('/api/v1/bid-opportunities/search', body, { timeout: 35000 })
  if (!response.success || !response.data) throw new Error('未能获取标讯，请重试。')
  return response.data
}
