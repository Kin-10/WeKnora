import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { AnonymousBidCheckReport, AnonymousBidCheckRequest } from '../../api/anonymous-bid-check'
import { AnonymousBidCheckRunner, MAX_CHECK_FILE_SIZE, parseIdentityKeywords, reportChecks, sortReportChecks, validateCheckFile, validateIdentityKeywords } from './checkState'

const report = (name = '技术标.docx'): AnonymousBidCheckReport => ({
  status: 'review', tender_file: '招标文件.pdf', bid_file: name, scope: 'technical',
  checks: [
    { id: '1', category: 'identity', title: '名称', status: 'fail', message: '', requirement: '', source_file: '', location: '段落 1', excerpt: '', suggestion: '' },
    { id: '2', category: 'format', title: '格式', status: 'review', message: '', requirement: '', source_file: '', location: '', excerpt: '', suggestion: '' },
    { id: '3', category: 'text', title: '文字', status: 'pass', message: '', requirement: '', source_file: '', location: '', excerpt: '', suggestion: '' },
  ],
  rules: [], limitations: [], summary: { failed: 1, review: 1, passed: 1 }, checked_at: '2026-10-08T00:00:00Z',
})
const request = (): AnonymousBidCheckRequest => ({ tenderFile: { name: '招标.pdf', size: 1 } as File, bidFile: { name: '标书.docx', size: 1 } as File, scope: 'technical', identityKeywords: ['企业'] })

test('upload validation rejects empty, unsupported and oversized documents; the 200 MiB boundary is accepted', () => {
  assert.equal(MAX_CHECK_FILE_SIZE, 200 * 1024 * 1024)
  assert.equal(validateCheckFile({ name: 'large-bid.pdf', size: 50 * 1024 * 1024 + 1 }), null)
  assert.equal(validateCheckFile({ name: 'bid.DOCX', size: MAX_CHECK_FILE_SIZE }), null)
  for (const name of ['bid.pdf', 'bid.doc', 'bid.txt', 'bid.md']) assert.equal(validateCheckFile({ name, size: 1 }), null)
  assert.equal(validateCheckFile({ name: 'bid.docx', size: MAX_CHECK_FILE_SIZE + 1 }), 'size')
  assert.equal(validateCheckFile({ name: 'bid.docx', size: 0 }), 'empty')
  assert.equal(validateCheckFile({ name: 'bid.docx.exe', size: 1 }), 'format')
})

test('identity keywords preserve complete names and deduplicate blank or repeated lines', () => {
  assert.deepEqual(parseIdentityKeywords(' 第一企业（集团）有限公司 \r\n张 三\n\n第一企业（集团）有限公司\n'), ['第一企业（集团）有限公司', '张 三'])
})

test('keyword limits match the server while counting Unicode characters', () => {
  assert.equal(validateIdentityKeywords([]), null)
  assert.equal(validateIdentityKeywords(['企业', '𠮷野']), null)
  assert.equal(validateIdentityKeywords(['张']), 'length')
  assert.equal(validateIdentityKeywords(['a'.repeat(101)]), 'length')
  assert.equal(validateIdentityKeywords(Array.from({ length: 51 }, (_, index) => `企业${index}`)), 'count')
})

test('report filtering keeps review separate from problems and passing checks', () => {
  const value = report()
  assert.deepEqual(reportChecks(value, 'all'), value.checks)
  for (const status of ['fail', 'review', 'pass'] as const) assert.deepEqual(reportChecks(value, status).map(check => check.status), [status])
})

test('report presentation puts issues first, preserving same-status order and all checks without mutating the report', () => {
  const value = report()
  const [fail, review, pass] = value.checks
  value.checks = [review!, pass!, fail!, { ...review!, id: 'review-2' }, { ...fail!, id: 'fail-2' }, { ...pass!, id: 'pass-2' }]
  const originalOrder = value.checks.map(check => check.id)
  assert.deepEqual(sortReportChecks(value.checks).map(check => check.id), ['1', 'fail-2', '2', 'review-2', '3', 'pass-2'])
  assert.deepEqual(value.checks.map(check => check.id), originalOrder)
  assert.equal(sortReportChecks(value.checks).length, value.checks.length)
})

test('replacing inputs aborts the request and discards a late response', async () => {
  let finish!: (report: AnonymousBidCheckReport) => void
  let signal!: AbortSignal
  const runner = new AnonymousBidCheckRunner(async (_, value) => { signal = value; return new Promise(resolve => { finish = resolve }) })
  const pending = runner.run(request(), 'failed')
  assert.equal(runner.loading, true)
  runner.reset()
  assert.equal(signal.aborted, true)
  finish(report())
  await pending
  assert.equal(runner.report, null)
  assert.equal(runner.loading, false)
  assert.equal(runner.error, '')
})

test('a superseded request cannot clear loading or overwrite the latest report', async () => {
  const finish: Array<(value: AnonymousBidCheckReport) => void> = []
  const runner = new AnonymousBidCheckRunner(async () => new Promise(resolve => finish.push(resolve)))
  const first = runner.run(request(), 'failed')
  const second = runner.run(request(), 'failed')
  finish[0]!(report('old.docx'))
  await first
  assert.equal(runner.loading, true)
  assert.equal(runner.report, null)
  finish[1]!(report('new.docx'))
  await second
  assert.equal((runner.report as AnonymousBidCheckReport | null)?.bid_file, 'new.docx')
  assert.equal(runner.loading, false)
})

test('failed requests expose an actionable error and can be retried', async () => {
  let attempts = 0
  const runner = new AnonymousBidCheckRunner(async () => { if (!attempts++) throw { message: '文件无法解析' }; return report() })
  await runner.run(request(), 'failed')
  assert.equal(runner.error, '文件无法解析')
  assert.equal(runner.report, null)
  await runner.run(request(), 'failed')
  assert.equal(runner.error, '')
  assert.equal((runner.report as AnonymousBidCheckReport | null)?.status, 'review')
})
