import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { createServer, type ViteDevServer } from 'vite'
import vue from '@vitejs/plugin-vue'
import { createSSRApp, h, type Component } from 'vue'
import { renderToString } from 'vue/server-renderer'
import type { BidOpportunity } from '../../api/bid-opportunities'

let server: ViteDevServer
let Card: Component
before(async () => {
  server = await createServer({ configFile: false, optimizeDeps: { noDiscovery: true, entries: [] }, plugins: [vue()],
    resolve: { alias: { '@': fileURLToPath(new URL('../../', import.meta.url)) } },
    server: { middlewareMode: true, hmr: false, ws: false }, appType: 'custom' })
  Card = (await server.ssrLoadModule('/src/views/bid-opportunities/BidOpportunityCard.vue')).default
})
after(async () => { await server?.close() })
async function render(item: BidOpportunity) {
  const app = createSSRApp({ render: () => h(Card, { item }) })
  app.component('t-icon', { render: () => h('i') })
  return renderToString(app)
}

test('search cards show amounts as provided and all unit names with explicit search summary action', async () => {
  const html = await render({ id: 'a', title: '<a>检验试剂采购</a>', mainBody: '<p>IVD 试剂采购项目</p>',
    bidType: '招标公告', bidUnit: { name: '采购单位一,采购单位二', units: [{ name: '采购单位一' }, { name: '采购单位二' }] },
    winBidUnit: { name: '中标单位' }, budgetAmount: '9.9万元', winBidAmount: '未公开',
    bidEndDate: '2026-10-18', bidRemainingDays: 10, publishDate: '2026-10-08 00:00:00' })
  assert.match(html, /检验试剂采购/)
  assert.match(html, /采购单位一、采购单位二/)
  assert.doesNotMatch(html, /采购单位一,采购单位二/)
  assert.match(html, /9\.9万元/)
  assert.match(html, /中标金额/)
  assert.match(html, /未公开/)
  assert.match(html, /剩余 10 天/)
  assert.match(html, /查看搜索摘要/)
  assert.doesNotMatch(html, /<a>/)
})

test('search cards escape encoded HTML and handle missing amounts, null units and expired deadlines', async () => {
  const html = await render({ id: 'b', title: '&lt;img src=x onerror=alert(1)&gt;', mainBody: '<script>bad()</script>普通摘要',
    bidUnit: null, winBidUnit: null, tags: [{ name: '<a>已截止</a>' }], bidEndDate: '2026-09-20', bidRemainingDays: -18 })
  assert.match(html, /&lt;img/)
  assert.match(html, /普通摘要/)
  assert.match(html, /已截止 18 天/)
  assert.doesNotMatch(html, /<img|<script|bad\(\)|预算金额|中标金额|采购单位/)
})
