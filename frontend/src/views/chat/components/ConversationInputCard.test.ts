import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { createServer, type ViteDevServer } from 'vite'
import vue from '@vitejs/plugin-vue'
import { createSSRApp, h, type Component } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createI18n } from 'vue-i18n'
import type { ConversationInputRequest } from '../../../utils/conversationInput'

const request: ConversationInputRequest = {
  id: 'bid-details', title: '请补充投标信息', questions: [
    { id: 'scope', label: '编制范围', type: 'single', required: true, options: [{ value: 'technical', label: '技术标' }, { value: 'all', label: '完整标书' }] },
    { id: 'materials', label: '已有资料', type: 'multiple', required: false, options: [{ value: 'license', label: '企业资质' }, { value: 'experience', label: '业绩证明' }] },
    { id: 'company', label: '投标单位', type: 'text', required: true, placeholder: '填写企业名称' },
  ],
}
const messages = { chat: { conversationInput: {
  introduction: '请选择或补充以下信息，提交后继续生成。', required: '必填', optional: '选填',
  multipleHint: '可多选', otherLabel: '补充其他内容', otherPlaceholder: '填写未列出的选项或补充说明',
  requiredError: '请选择或填写此项。', submit: '确认并继续', sending: '正在发送…', answered: '已进入后续对话',
} } }
let server: ViteDevServer
let Card: Component
let collect: (request: ConversationInputRequest, answers: Record<string, string | string[]>, supplements: Record<string, string>) => {
  values: Record<string, string | string[]>
  missing: string[]
}

before(async () => {
  server = await createServer({
    configFile: false, optimizeDeps: { noDiscovery: true, entries: [] }, plugins: [vue()],
    resolve: { alias: { '@': fileURLToPath(new URL('../../../', import.meta.url)) } },
    server: { middlewareMode: true, hmr: false, ws: false }, appType: 'custom',
  })
  const loaded = await server.ssrLoadModule('/src/views/chat/components/ConversationInputCard.vue')
  Card = loaded.default
  collect = loaded.collectConversationInputValues
})
after(async () => { await server?.close() })
async function render(props: Record<string, unknown> = {}) {
  const app = createSSRApp({ render: () => h(Card, { request, ...props }) })
  app.use(createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': messages } }))
  return renderToString(app)
}

test('required fields are validated together while blank optional fields are omitted', () => {
  assert.deepEqual(collect(request, {}, {}), { values: {}, missing: ['scope', 'company'] })
  assert.deepEqual(collect(request, { scope: 'technical', company: '  测试企业  ' }, {}), {
    values: { scope: 'technical', company: '测试企业' }, missing: [],
  })
})

test('free text can replace any choice or supplement selected choices', () => {
  assert.deepEqual(collect(request, { company: '测试企业' }, { scope: '  仅商务标  ' }), {
    values: { scope: '仅商务标', company: '测试企业' }, missing: [],
  })
  assert.deepEqual(collect(request, { scope: 'all', materials: ['license', 'experience'], company: '测试企业' }, {
    scope: '分册输出', materials: '另有授权书',
  }), {
    values: { scope: ['all', '分册输出'], materials: ['license', 'experience', '另有授权书'], company: '测试企业' }, missing: [],
  })
})

test('invalid selection values and duplicates do not bypass required validation', () => {
  assert.deepEqual(collect(request, { scope: 'unknown', materials: ['license', 'unknown', 'license'], company: '   ' }, {}), {
    values: { materials: ['license'] }, missing: ['scope', 'company'],
  })
})

test('card provides labeled single, multiple and text inputs inline', async () => {
  const html = await render()
  assert.match(html, /请补充投标信息/)
  assert.equal((html.match(/type="radio"/g) || []).length, 2)
  assert.equal((html.match(/type="checkbox"/g) || []).length, 2)
  assert.equal((html.match(/<textarea/g) || []).length, 3)
  assert.match(html, /aria-label="投标单位"/)
  assert.match(html, /aria-required="true"/)
  assert.match(html, /补充其他内容/)
  assert.match(html, /确认并继续/)
  assert.doesNotMatch(html, /role="dialog"/)
})

test('sending disables fields and historical card describes later conversation without claiming submission', async () => {
  const busy = await render({ disabled: true })
  assert.equal((busy.match(/<fieldset[^>]* disabled/g) || []).length, 3)
  assert.match(busy, /<button[^>]*disabled/)
  assert.match(busy, /正在发送/)
  const answered = await render({ answered: true })
  assert.match(answered, /已进入后续对话/)
  assert.doesNotMatch(answered, /<form|<input|<textarea|已提交/)
})

test('model supplied titles and labels are escaped and field identifiers are generated locally', async () => {
  const unsafe = { ...request, title: '<script>alert(1)</script>', questions: [{ ...request.questions[0]!, label: '<img src=x onerror=alert(1)>', id: 'unsafe' }] }
  const html = await render({ request: unsafe })
  assert.match(html, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/)
  assert.match(html, /&lt;img src=x onerror=alert\(1\)&gt;/)
  assert.doesNotMatch(html, /<script|<img|id="unsafe/)
})
