import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'
import ts from 'typescript'
import { parse, compileStyleAsync } from '@vue/compiler-sfc'

const source = readFileSync(new URL('./document-preview.vue', import.meta.url), 'utf8')
const start = source.indexOf('function fitDocxPages()')
const end = source.indexOf('let docxResizeObserver:', start)
assert.ok(start >= 0 && end > start)
const productionFit = ts.transpile(source.slice(start, end))

function setup(width, pageWidths = [794]) {
  const pages = pageWidths.map(offsetWidth => ({ offsetWidth, style: { width: `${offsetWidth}px`, padding: '72pt' } }))
  const wrapper = { style: {}, querySelectorAll: selector => {
    assert.equal(selector, 'section.docx-preview-wrapper')
    return pages
  } }
  const root = { clientWidth: width, querySelector: selector => {
    assert.equal(selector, '.docx-preview-wrapper-wrapper')
    return wrapper
  } }
  const fit = vm.runInNewContext(`${productionFit}; fitDocxPages`, { docxContainer: { value: root } })
  return { root, wrapper, pages, fit }
}

test('a narrow sidebar scales real A4 geometry rather than shrinking the text column', () => {
  const h = setup(320)
  const pageGeometry = h.pages.map(page => ({ ...page.style }))
  h.fit()
  assert.equal(h.wrapper.style.width, '834px')
  assert.equal(Number(h.wrapper.style.zoom), 320 / 834)
  assert.deepEqual(h.pages.map(page => page.style), pageGeometry)
  // Page width, its 96px margins and font size share the same multiplier.
  const scale = Number(h.wrapper.style.zoom)
  assert.ok(Math.abs((794 + 40) * scale - 320) < 0.001)
  assert.ok(Math.abs((794 - 2 * 96) * scale / (794 * scale) - 602 / 794) < 1e-10)
})

test('desktop/fullscreen restores 100 percent without enlarging or changing page geometry', () => {
  const h = setup(320)
  h.fit()
  h.root.clientWidth = 1400
  h.fit()
  assert.equal(h.wrapper.style.zoom, '1')
  assert.equal(h.wrapper.style.width, '834px')
  assert.equal(h.pages[0].style.padding, '72pt')
  h.root.clientWidth = 460
  h.fit()
  assert.equal(Number(h.wrapper.style.zoom), 460 / 834)
})

test('mixed portrait/landscape documents use the widest page so neither page is clipped', () => {
  const h = setup(600, [794, 1123, 794])
  h.fit()
  assert.equal(h.wrapper.style.width, '1163px')
  assert.equal(Number(h.wrapper.style.zoom), 600 / 1163)
  assert.deepEqual(h.pages.map(page => page.offsetWidth), [794, 1123, 794])
})

test('hidden or not yet rendered previews do not acquire a zero zoom', () => {
  const hidden = setup(0)
  hidden.fit()
  assert.deepEqual(hidden.wrapper.style, {})
  const empty = setup(320, [])
  empty.fit()
  assert.deepEqual(empty.wrapper.style, {})
})

test('compiled preview CSS styles only the outer canvas and leaves Word widths/tables intact', async () => {
  const { descriptor } = parse(source)
  const result = await compileStyleAsync({
    source: descriptor.styles[0].content, filename: 'document-preview.vue', id: 'preview', preprocessLang: 'less',
  })
  assert.deepEqual(result.errors, [])
  const docxRules = [...result.code.matchAll(/([^{}]*\.docx-preview-wrapper[^{}]*)\{([^}]+)\}/g)]
  assert.ok(docxRules.length)
  for (const [, selector, css] of docxRules) {
    assert.match(selector, /\.docx-preview-wrapper-wrapper/)
    assert.doesNotMatch(css, /max-width:\s*100%|width:\s*100%|table-layout:/)
  }
  assert.match(source, /new ResizeObserver\(fitDocxPages\)/)
  assert.match(source, /querySelectorAll\('section > article'\)/, 'citation locating keeps the original page DOM')
})
