import assert from 'node:assert/strict'
import test from 'node:test'
import { documentFormattingLabel, documentFormattingSuccess, documentFormattingSourceLabel, readDocumentFormatting } from './documentFormatting.ts'

const translate = (key: string, values?: Record<string, string>) => values?.sources ? `${key}: ${values.sources}` : key

test('formatting metadata accepts only known modes and scopes', () => {
  for (const value of [null, [], 'tender', { mode: 'compliant', scope: 'business' },
    { mode: 'tender', scope: 'both' }]) assert.equal(readDocumentFormatting(value), undefined)
  assert.deepEqual(readDocumentFormatting({ mode: 'tender', scope: 'technical', source_files: ['当前招标.pdf', 10],
    summary: ['宋体四号', null], warning: '尚有要求需核对' }), {
    mode: 'tender', scope: 'technical', source_files: ['当前招标.pdf'],
    summary: ['宋体四号'], warning: '尚有要求需核对',
  })
})

test('labels state the source and defaults without claiming compliance', () => {
  const tender = readDocumentFormatting({ mode: 'tender', scope: 'business', source_files: ['当前招标.pdf'], summary: [] })!
  assert.equal(documentFormattingLabel(tender, translate), 'chat.wordFormattingTender: 当前招标.pdf')
  assert.equal(documentFormattingSuccess(tender, translate), 'chat.wordGeneratedFromTender: 当前招标.pdf')
  assert.equal(documentFormattingLabel({ ...tender, mode: 'default' }, translate), 'chat.wordFormattingDefault')
  assert.equal(documentFormattingLabel({ ...tender, mode: 'blocked' }, translate), 'chat.wordFormattingBlocked')
})

test('checked sources remain available for default and blocked volumes', () => {
  for (const mode of ['default', 'blocked', 'tender'] as const) {
    const formatting = readDocumentFormatting({ mode, scope: 'business',
      source_files: ['核酸招标文件.pdf'], summary: [], warning: '采用默认样式' })!
    assert.equal(documentFormattingSourceLabel(formatting, translate), 'chat.wordFormattingCheckedSources: 核酸招标文件.pdf')
    assert.equal(formatting.scope, 'business')
  }
  assert.equal(documentFormattingSourceLabel({ mode: 'default', scope: 'technical', source_files: [], summary: [] }, translate), '')
})
