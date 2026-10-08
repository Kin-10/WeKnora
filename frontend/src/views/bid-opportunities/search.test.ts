import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { BidOpportunity, BidSearchPage, BidSearchRequest } from '../../api/bid-opportunities'
import { MAX_SEARCH_PAGES, BidSearchPager, bidLocation, bidText, buildBidSearchRequest, defaultBidFilters, searchError, sourceLink, unitNames } from './search'

const item = (id: string, extra: Partial<BidOpportunity> = {}): BidOpportunity => ({ id, title: `项目${id}`, ...extra })
const page = (items: BidOpportunity[], total: number | null = null, cursor = 'cursor-next'): BidSearchPage => ({
  items, total, page_num: 1, page_size: 20, next_time: cursor, trace_id: 'trace', upstream_code: 200,
})

test('search builds the documented fields, keeps region names and makes dates exclusive with presets', () => {
  const filters = { ...defaultBidFilters(), keyword: ' IVD试剂 ', province: '河北省，河北省、北京市', city: '石家庄市',
    county: '长安区;桥西区', dateRange: ['2026-10-01', '2026-10-08'], timeInterval: 3, bidTypes: ['招标公告'] }
  const request = buildBidSearchRequest(filters, 2, 'server cursor')
  assert.deepEqual(request, { keyword: 'IVD试剂', provinces: ['河北省', '北京市'], cities: ['石家庄市'], counties: ['长安区', '桥西区'],
    bidTypes: ['招标公告'], startTime: '2026-10-01 00:00:00', endTime: '2026-10-08 23:59:59', timeInterval: 0,
    matchType: 'term', orderBy: 'PUBLISH_DATE_DESC', pageNum: 2, pageSize: 20, time: 'server cursor' })
  filters.bidTypes.push('中标结果')
  assert.deepEqual(request.bidTypes, ['招标公告'], 'the running query owns a snapshot')
  assert.equal(buildBidSearchRequest(defaultBidFilters()).time, '')
})

test('upstream highlighted HTML is plain text; unit collections and region nulls are handled', () => {
  assert.equal(bidText('<p><a style="color:red" onclick="bad()">IVD</a>&nbsp;试剂</p><script>alert(1)</script>'), 'IVD 试剂')
  assert.equal(bidText('&#x1F4C4; &amp; &lt;img src=x&gt;'), '📄 & <img src=x>')
  assert.equal(bidText('&#999999999; &#55296;'), '')
  assert.equal(unitNames({ name: '采购单位', units: [{ name: '采购单位' }, { name: '另一单位' }, { nameAlias: '简称' }] }), '采购单位、另一单位、简称')
  assert.equal(unitNames({ name: '单位一,单位二', units: [{ name: '单位一' }, { name: '单位二' }, { name: '单位一' }] }), '单位一、单位二')
  assert.equal(unitNames({ name: '采购单位', units: [{ name: '' }] }), '采购单位')
  assert.equal(unitNames(null), '')
  assert.equal(bidLocation(item('a', { province: '北京市', city: '北京市', region: null })), '北京市')
})

test('only complete http(s) source URLs without credentials are links', () => {
  assert.equal(sourceLink('https://example.org/notice?id=1'), 'https://example.org/notice?id=1')
  assert.equal(sourceLink('http://example.org/'), 'http://example.org/')
  for (const source of ['', null, '/notice/1', 'javascript:alert(1)', '//example.org', 'data:text/html,a',
    'https://name:secret@example.org', 'https://user@example.org', 'https://example.org/\nnotice', 'https://']) {
    assert.equal(sourceLink(source), '', String(source))
  }
})

test('the pager has no initial request, follows the cursor and caches sequential previous pages', async () => {
  const requests: BidSearchRequest[] = []
  const pager = new BidSearchPager(async request => {
    requests.push(request)
    return request.pageNum === 1 ? page([item('a')], null, 'cursor-1') : page([item('b')], null, 'cursor-2')
  })
  assert.equal(requests.length, 0)
  await pager.search(defaultBidFilters())
  assert.equal(pager.hasNext, true, 'a short nonempty page does not establish an upstream page limit')
  await pager.next()
  assert.equal(requests[1]?.time, 'cursor-1')
  assert.equal(requests[1]?.pageNum, 2)
  assert.equal(pager.total, null)
  assert.equal(pager.loadedCount, 2)
  pager.previous()
  assert.equal(pager.items[0]?.id, 'a')
  await pager.next()
  assert.equal(pager.items[0]?.id, 'b')
  assert.equal(requests.length, 2, 'cached navigation preserves the original cursor chain')
})

test('cross-page IDs are deduplicated and an empty page stops at the last visible page', async () => {
  const responses = [page([item('a'), item('a')]), page([item('a'), item('b')]), page([])]
  const pager = new BidSearchPager(async () => responses.shift()!)
  await pager.search(defaultBidFilters())
  assert.equal(pager.items.length, 1)
  await pager.next()
  assert.deepEqual(pager.items.map(value => value.id), ['b'])
  await pager.next()
  assert.equal(pager.currentPage, 2)
  assert.equal(pager.hasNext, false)
  assert.equal(pager.stopReason, 'empty')
  assert.equal(pager.loadedCount, 2)
})

test('fully repeated pages stop without dropping results; a known total stops once reached', async () => {
  const repeat = new BidSearchPager(async () => page([item('a')]))
  await repeat.search(defaultBidFilters()); await repeat.next()
  assert.equal(repeat.stopReason, 'duplicates')
  assert.equal(repeat.currentPage, 1)
  assert.equal(repeat.items.length, 1)
  assert.equal(repeat.hasNext, false)
  const known = new BidSearchPager(async () => page([item('a')], 1))
  await known.search(defaultBidFilters())
  assert.equal(known.stopReason, 'total')
  assert.equal(known.hasNext, false)
})

test('changing filters resets the page and cursor and ignores an old in-flight response', async () => {
  let resolveOld!: (value: BidSearchPage) => void
  const requests: BidSearchRequest[] = []
  const pager = new BidSearchPager(request => {
    requests.push(request)
    return request.keyword === '旧查询' ? new Promise(resolve => { resolveOld = resolve }) : Promise.resolve(page([item('new')]))
  })
  const old = pager.search({ ...defaultBidFilters(), keyword: '旧查询' })
  pager.reset()
  assert.equal(pager.loading, false)
  await pager.search({ ...defaultBidFilters(), keyword: '新查询' })
  resolveOld(page([item('old')], 900, 'stale'))
  await old
  assert.deepEqual(pager.items.map(value => value.id), ['new'])
  assert.equal(pager.total, null)
  assert.equal(pager.loading, false)
  assert.equal(requests[1]?.pageNum, 1)
  assert.equal(requests[1]?.time, '')
})

test('failed next-page requests preserve results and retry the same cursor with the trace ID', async () => {
  const requests: BidSearchRequest[] = []
  let fail = true
  const pager = new BidSearchPager(async request => {
    requests.push(request)
    if (request.pageNum === 2 && fail) { fail = false; throw { error: { message: '服务暂时不可用', details: { trace_id: 'trace-123' } } } }
    return page([item(request.pageNum === 1 ? 'a' : 'b')], null, 'cursor-1')
  })
  await pager.search(defaultBidFilters()); await pager.next()
  assert.equal(pager.currentPage, 1)
  assert.equal(pager.items[0]?.id, 'a')
  assert.equal(pager.error?.traceId, 'trace-123')
  await pager.retry()
  assert.equal(pager.currentPage, 2)
  assert.equal(pager.error, null)
  assert.deepEqual(requests[2], requests[1])
  assert.equal(searchError(null).message, '搜索暂时不可用，请稍后重试。')
})

test('the local product page cap bounds an unknown total without treating it as an upstream contract', async () => {
  const pager = new BidSearchPager(async request => page([item(String(request.pageNum))], null, String(request.pageNum)))
  await pager.search(defaultBidFilters())
  for (let i = 1; i < MAX_SEARCH_PAGES; i++) await pager.next()
  assert.equal(pager.currentPage, MAX_SEARCH_PAGES)
  assert.equal(pager.hitLimit, true)
  assert.equal(pager.stopReason, 'limit')
  assert.equal(pager.hasNext, false)
  await pager.next()
  assert.equal(pager.loadedCount, MAX_SEARCH_PAGES)
})
