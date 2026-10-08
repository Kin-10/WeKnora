import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { createServer, type ViteDevServer } from 'vite'
import vue from '@vitejs/plugin-vue'
import { createSSRApp, h, type Component } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createI18n } from 'vue-i18n'
import zh from '../../../i18n/locales/zh-CN'
import type { BidGenerationTask } from '@/api/bid-generation'

let server: ViteDevServer
let Progress: Component
let InputCard: Component
before(async () => {
  server = await createServer({
    configFile: false, optimizeDeps: { noDiscovery: true, entries: [] }, plugins: [vue()],
    resolve: { alias: { '@': fileURLToPath(new URL('../../../', import.meta.url)) } },
    server: { middlewareMode: true, hmr: false, ws: false }, appType: 'custom',
  })
  Progress = (await server.ssrLoadModule('/src/views/chat/components/BidGenerationProgress.vue')).default
  InputCard = (await server.ssrLoadModule('/src/views/chat/components/ConversationInputCard.vue')).default
})
after(async () => { await server?.close() })

async function render(status: BidGenerationTask['status'], extra: Partial<BidGenerationTask> = {}, busy = false, supplement = false) {
  const task: BidGenerationTask = { id: 'task', session_id: 'session', revision: 3, title: '测试标书', status, phase: 'writing',
    sections: [{ id: 'a', title: '商务文件', status: 'completed', word_count: 8000 }, { id: 'b', title: '技术方案', status: 'writing', word_count: 0 }],
    current_section: 1, total_words: 8000, ...extra }
  const request = { id: 'delivery', title: '补充本章交付数据', questions: [
    { id: 'days', label: '交付期限', type: 'text', required: true },
  ] }
  const app = createSSRApp({ render: () => h(Progress, { task, busy }, supplement
    ? { supplement: () => h(InputCard, { request }) } : undefined) })
  app.use(createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': zh } }))
  return renderToString(app)
}
test('outline confirmation stays inline with its outline and permits changes through the composer', async () => {
  const html = await render('awaiting_outline')
  assert.match(html, /待确认目录/)
  assert.match(html, /直接在对话框中说明/)
  assert.match(html, /确认目录并开始生成/)
  assert.doesNotMatch(html, /role="dialog"|继续生成|下载完整标书 Word/)
})
test('chapter progress shows totals, current chapter and pause without requiring a manual next chapter', async () => {
  const html = await render('running')
  assert.match(html, /已完成 1\/2 章 · 已生成 8000 字/)
  assert.match(html, /正在生成：技术方案/)
  assert.match(html, /<progress[^>]+value="1"[^>]+max="2"/)
  assert.match(html, />暂停<\/button>/)
  assert.doesNotMatch(html, /下一章|下载完整标书 Word/)
})
test('waiting, pause and errors offer the appropriate chat and recovery actions', async () => {
  const waiting = await render('awaiting_input')
  assert.match(waiting, /选择或补充所需信息/)
  assert.doesNotMatch(waiting, /确认目录并开始生成|>暂停<\/button>/)
  const paused = await render('paused')
  assert.match(paused, /继续生成/)
  const failed = await render('failed', { error: '模型暂时不可用 <script>alert(1)</script>' })
  assert.match(failed, /重试/)
  assert.match(failed, /取消生成/)
  assert.match(failed, /&lt;script&gt;/)
  assert.doesNotMatch(failed, /<script/)
})
test('the supplement form appears after the chapter list and before task controls', async () => {
  const html = await render('awaiting_input', {}, false, true)
  assert.ok(html.indexOf('class="section-list"') < html.indexOf('补充本章交付数据'))
  assert.ok(html.indexOf('确认并继续') < html.indexOf('class="progress-actions"'))
  assert.match(html, /class="progress-supplement"[^>]*data-section-id="b"/)
  assert.equal((html.match(/class="conversation-input-card"/g) || []).length, 1)
  assert.match(html, /<textarea/)
})
test('the complete job downloads its actual Word artifact and disables controls during a request', async () => {
  const html = await render('completed', { artifact_message_id: 'final-message', artifact_index: 0, artifact_file_name: '完整标书.docx' }, true)
  assert.match(html, /下载完整标书 Word/)
  assert.match(html, /<button[^>]+disabled/)
  assert.doesNotMatch(html, /重试|取消生成|确认目录并开始生成/)
  const noFile = await render('completed')
  assert.doesNotMatch(noFile, /下载完整标书 Word/)
})
