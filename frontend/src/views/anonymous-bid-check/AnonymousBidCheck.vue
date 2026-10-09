<template>
  <main class="anonymous-bid-check-page" @dragenter.stop @dragover.stop.prevent @drop.stop.prevent>
    <header class="page-header" style="--wails-draggable: drag">
      <h2><t-icon name="file-search" size="24px" />{{ t('anonymousBidCheck.title') }}</h2>
      <p>{{ t('anonymousBidCheck.description') }}</p>
    </header>

    <section class="check-form" :aria-label="t('anonymousBidCheck.uploadHeading')">
      <div class="document-grid">
        <div class="document-field">
          <label for="anonymous-tender-file"><span class="step-number">01</span>{{ t('anonymousBidCheck.tenderFile') }}</label>
          <p>{{ t('anonymousBidCheck.tenderHint') }}</p>
          <input id="anonymous-tender-file" ref="tenderInput" type="file" :accept="CHECK_FILE_ACCEPT" :disabled="runner.loading" @change="selectFile('tender', $event)" />
          <div v-if="tenderFile" class="selected-document"><span :title="tenderFile.name">{{ tenderFile.name }} <small>{{ checkFileSize(tenderFile.size) }}</small></span><t-button size="small" theme="default" variant="text" :disabled="runner.loading" :aria-label="t('anonymousBidCheck.removeTender')" @click="removeFile('tender')">{{ t('anonymousBidCheck.remove') }}</t-button></div>
        </div>
        <div class="document-field">
          <label for="anonymous-bid-file"><span class="step-number">02</span>{{ t('anonymousBidCheck.bidFile') }}</label>
          <p>{{ t('anonymousBidCheck.bidHint') }}</p>
          <input id="anonymous-bid-file" ref="bidInput" type="file" :accept="CHECK_FILE_ACCEPT" :disabled="runner.loading" @change="selectFile('bid', $event)" />
          <div v-if="bidFile" class="selected-document"><span :title="bidFile.name">{{ bidFile.name }} <small>{{ checkFileSize(bidFile.size) }}</small></span><t-button size="small" theme="default" variant="text" :disabled="runner.loading" :aria-label="t('anonymousBidCheck.removeBid')" @click="removeFile('bid')">{{ t('anonymousBidCheck.remove') }}</t-button></div>
        </div>
      </div>
      <p class="format-note"><t-icon name="info-circle" size="15px" />{{ t('anonymousBidCheck.formats') }}</p>
      <div class="check-options">
        <div class="scope-field">
          <label id="anonymous-scope-label">{{ t('anonymousBidCheck.scopeLabel') }}</label>
          <t-radio-group v-model="scope" :disabled="runner.loading" aria-labelledby="anonymous-scope-label">
            <t-radio-button value="technical">{{ t('anonymousBidCheck.scope.technical') }}</t-radio-button>
            <t-radio-button value="business">{{ t('anonymousBidCheck.scope.business') }}</t-radio-button>
            <t-radio-button value="all">{{ t('anonymousBidCheck.scope.all') }}</t-radio-button>
          </t-radio-group>
          <p>{{ t('anonymousBidCheck.scopeHint') }}</p>
        </div>
        <div class="keywords-field">
          <label for="anonymous-identity-keywords">{{ t('anonymousBidCheck.identityKeywords') }}<span>{{ t('anonymousBidCheck.optional') }}</span></label>
          <textarea id="anonymous-identity-keywords" v-model="identityText" :disabled="runner.loading" :placeholder="t('anonymousBidCheck.identityPlaceholder')" rows="3" maxlength="10000" />
          <p>{{ t('anonymousBidCheck.identityHint') }}</p>
        </div>
      </div>
      <div class="submit-row">
        <p>{{ t('anonymousBidCheck.coverage') }}</p>
        <t-button theme="primary" :loading="runner.loading" :disabled="!tenderFile || !bidFile" @click="runCheck"><t-icon name="check-circle" size="16px" />{{ runner.loading ? t('anonymousBidCheck.checking') : t('anonymousBidCheck.start') }}</t-button>
      </div>
    </section>

    <div v-if="runner.error" class="check-error" role="alert"><t-icon name="error-circle" size="18px" /><span>{{ runner.error }}</span></div>
    <div v-if="runner.loading" class="loading-state" role="status" aria-live="polite"><t-loading size="small" /><div><strong>{{ t('anonymousBidCheck.checking') }}</strong><p>{{ t('anonymousBidCheck.loadingHint') }}</p></div></div>

    <section v-if="runner.report" ref="reportElement" class="check-report" :aria-label="t('anonymousBidCheck.reportTitle')" aria-live="polite">
      <header class="report-heading">
        <div><h3>{{ t('anonymousBidCheck.reportTitle') }}<span class="status-chip" :class="`status-${runner.report.status}`">{{ statusLabel(runner.report.status) }}</span></h3><p>{{ t('anonymousBidCheck.checkedAt', { time: checkedTime }) }}</p></div>
        <div class="report-actions"><t-button theme="default" variant="outline" size="small" @click="exportJson"><t-icon name="download" size="15px" />{{ t('anonymousBidCheck.exportJson') }}</t-button><t-button theme="default" variant="outline" size="small" @click="printReport"><t-icon name="print" size="15px" />{{ t('anonymousBidCheck.print') }}</t-button></div>
      </header>
      <dl class="report-files"><div><dt>{{ t('anonymousBidCheck.tenderFile') }}</dt><dd>{{ runner.report.tender_file }}</dd></div><div><dt>{{ t('anonymousBidCheck.bidFile') }}</dt><dd>{{ runner.report.bid_file }}</dd></div><div><dt>{{ t('anonymousBidCheck.scopeLabel') }}</dt><dd>{{ scopeLabel(runner.report.scope) }}</dd></div></dl>
      <div class="summary-grid">
        <div class="summary-stat stat-fail"><strong>{{ runner.report.summary.failed }}</strong><span>{{ t('anonymousBidCheck.status.fail') }}</span></div>
        <div class="summary-stat stat-review"><strong>{{ runner.report.summary.review }}</strong><span>{{ t('anonymousBidCheck.status.review') }}</span></div>
        <div class="summary-stat stat-pass"><strong>{{ runner.report.summary.passed }}</strong><span>{{ t('anonymousBidCheck.status.pass') }}</span></div>
      </div>
      <div class="coverage-notice"><strong>{{ t('anonymousBidCheck.limitationsTitle') }}</strong><p>{{ t('anonymousBidCheck.reportDisclaimer') }}</p><ul v-if="runner.report.limitations.length"><li v-for="(limitation, index) in runner.report.limitations" :key="index">{{ limitation }}</li></ul></div>

      <div class="report-filter" :aria-label="t('anonymousBidCheck.filterLabel')">
        <button v-for="value in statusFilters" :key="value" type="button" :class="{ selected: filter === value }" :aria-pressed="filter === value" @click="filter = value">{{ value === 'all' ? t('anonymousBidCheck.allChecks') : statusLabel(value) }}</button>
      </div>
      <p v-if="!visibleChecks.length" class="no-checks">{{ t('anonymousBidCheck.noMatchingChecks') }}</p>
      <div class="check-list">
        <article v-for="check in sortedChecks" :key="check.id" class="check-item" :class="[`check-${check.status}`, { 'check-filtered-out': filter !== 'all' && filter !== check.status }]">
          <div class="check-heading"><span class="status-chip" :class="`status-${check.status}`">{{ statusLabel(check.status) }}</span><h4>{{ check.title }}</h4></div>
          <p class="check-message">{{ check.message }}</p>
          <dl class="check-evidence">
            <div v-if="check.location"><dt>{{ t('anonymousBidCheck.location') }}</dt><dd>{{ check.location }}</dd></div>
            <div v-if="check.excerpt"><dt>{{ t('anonymousBidCheck.excerpt') }}</dt><dd class="evidence-quote">{{ check.excerpt }}</dd></div>
            <div><dt>{{ t('anonymousBidCheck.requirement') }}</dt><dd><blockquote v-if="check.requirement">{{ check.requirement }}</blockquote><span v-else class="missing-source">{{ t('anonymousBidCheck.noRequirement') }}</span><small v-if="check.source_file" class="source-caption">{{ check.source_file }}<template v-if="check.source_page"> · {{ t('anonymousBidCheck.sourcePage', { page: check.source_page }) }}</template></small></dd></div>
            <div v-if="check.suggestion"><dt>{{ t('anonymousBidCheck.suggestion') }}</dt><dd>{{ check.suggestion }}</dd></div>
          </dl>
        </article>
      </div>

      <details class="tender-rules" :open="printing">
        <summary>{{ t('anonymousBidCheck.rulesTitle', { count: runner.report.rules.length }) }}</summary>
        <ol v-if="runner.report.rules.length"><li v-for="rule in runner.report.rules" :key="rule.id"><p>{{ rule.requirement }}</p><small>{{ rule.source_file }}<template v-if="rule.source_page"> · {{ t('anonymousBidCheck.sourcePage', { page: rule.source_page }) }}</template></small></li></ol>
        <p v-else>{{ t('anonymousBidCheck.noRules') }}</p>
      </details>
    </section>
    <div v-else-if="!runner.loading && !runner.error" class="ready-state"><t-icon name="file-search" size="30px" /><strong>{{ t('anonymousBidCheck.readyTitle') }}</strong><p>{{ t('anonymousBidCheck.readyHint') }}</p></div>
  </main>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, reactive, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { checkAnonymousBid, type AnonymousBidCheckScope, type AnonymousBidCheckStatus } from '@/api/anonymous-bid-check'
import { AnonymousBidCheckRunner, CHECK_FILE_ACCEPT, checkFileSize, parseIdentityKeywords, reportChecks, sortReportChecks, validateCheckFile, validateIdentityKeywords } from './checkState'

const { t, locale } = useI18n()
const tenderFile = shallowRef<File | null>(null)
const bidFile = shallowRef<File | null>(null)
const tenderInput = ref<HTMLInputElement | null>(null)
const bidInput = ref<HTMLInputElement | null>(null)
const reportElement = ref<HTMLElement | null>(null)
const scope = ref<AnonymousBidCheckScope>('technical')
const identityText = ref('')
const runner = reactive(new AnonymousBidCheckRunner(checkAnonymousBid))
const filter = ref<AnonymousBidCheckStatus | 'all'>('all')
const printing = ref(false)
const statusFilters = ['all', 'fail', 'review', 'pass'] as const
const visibleChecks = computed(() => runner.report ? reportChecks(runner.report, filter.value) : [])
const sortedChecks = computed(() => runner.report ? sortReportChecks(runner.report.checks) : [])
const statusLabel = (status: AnonymousBidCheckStatus) => t(`anonymousBidCheck.status.${status}`)
const scopeLabel = (value: AnonymousBidCheckScope) => t(`anonymousBidCheck.scope.${value}`)
const checkedTime = computed(() => {
  const value = runner.report?.checked_at
  if (!value) return ''
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString(locale.value)
})

watch([tenderFile, bidFile, scope, identityText], () => { runner.reset(); filter.value = 'all' }, { flush: 'sync' })
onBeforeUnmount(() => runner.reset())

function selectFile(kind: 'tender' | 'bid', event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  runner.reset()
  const validation = validateCheckFile(file)
  const destination = kind === 'tender' ? tenderFile : bidFile
  if (validation) {
    destination.value = null
    input.value = ''
    runner.error = t(`anonymousBidCheck.fileErrors.${validation}`)
    return
  }
  destination.value = file
}

function removeFile(kind: 'tender' | 'bid') {
  const destination = kind === 'tender' ? tenderFile : bidFile
  const input = kind === 'tender' ? tenderInput : bidInput
  destination.value = null
  if (input.value) input.value.value = ''
}

async function runCheck() {
  if (!tenderFile.value || !bidFile.value || runner.loading) return
  filter.value = 'all'
  const identityKeywords = parseIdentityKeywords(identityText.value)
  const keywordError = validateIdentityKeywords(identityKeywords)
  if (keywordError) { runner.error = t(`anonymousBidCheck.keywordErrors.${keywordError}`); return }
  await runner.run({ tenderFile: tenderFile.value, bidFile: bidFile.value, scope: scope.value, identityKeywords }, t('anonymousBidCheck.failed'))
  if (runner.report) {
    await nextTick()
    reportElement.value?.scrollIntoView({ block: 'start' })
  }
}

function exportJson() {
  if (!runner.report) return
  const blob = new Blob([JSON.stringify(runner.report, null, 2)], { type: 'application/json;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = `wenshu-anonymous-bid-check-${new Date().toISOString().slice(0, 10)}.json`
  document.body.appendChild(link)
  link.click()
  link.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

async function printReport() {
  printing.value = true
  await nextTick()
  window.print()
  printing.value = false
}
</script>

<style scoped lang="less">
.anonymous-bid-check-page { flex: 1; min-height: 0; overflow-y: auto; padding: 20px 28px 32px; box-sizing: border-box; color: var(--td-text-color-primary); }
.page-header { margin-bottom: 20px; h2 { display: flex; align-items: center; gap: 8px; margin: 0; font-size: var(--app-text-4xl); font-weight: 600; line-height: 32px; } p { margin: 4px 0 0; font-size: 13px; line-height: 21px; color: var(--td-text-color-secondary); } }
.check-form { padding: 20px; border: 1px solid var(--td-component-stroke); border-radius: 10px; background: var(--td-bg-color-container); }
.document-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }
.document-field { min-width: 0; padding: 16px; background: var(--td-bg-color-secondarycontainer); border: 1px solid var(--td-component-stroke); border-radius: 8px; label { display: flex; align-items: center; gap: 8px; font-size: 14px; font-weight: 600; } p { margin: 8px 0 14px; font-size: 12px; line-height: 20px; color: var(--td-text-color-secondary); } input { display: block; width: 100%; min-width: 0; font-family: inherit; font-size: 12px; color: var(--td-text-color-secondary); cursor: pointer; &::file-selector-button { margin-right: 10px; padding: 6px 12px; border: 1px solid var(--td-component-stroke); border-radius: 5px; background: var(--td-bg-color-container); color: var(--td-text-color-primary); font-family: inherit; cursor: pointer; } &:focus-visible { outline: 2px solid var(--td-brand-color); outline-offset: 3px; } &:disabled { cursor: default; opacity: .65; } } }
.step-number { color: var(--td-brand-color); font-size: 12px; }
.selected-document { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-top: 10px; font-size: 12px; > span { min-width: 0; overflow-wrap: anywhere; } small { margin-left: 6px; color: var(--td-text-color-secondary); } .t-button { flex-shrink: 0; } }
.format-note { display: flex; align-items: flex-start; gap: 6px; color: var(--td-text-color-secondary); font-size: 12px; line-height: 20px; margin: 12px 0 20px; > .t-icon { flex-shrink: 0; margin-top: 2px; } }
.check-options { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 24px; label { display: block; font-size: 13px; font-weight: 500; margin-bottom: 10px; } p { color: var(--td-text-color-secondary); font-size: 12px; line-height: 20px; margin: 8px 0 0; } }
.keywords-field { label > span { margin-left: 8px; font-weight: 400; color: var(--td-text-color-placeholder); font-size: 12px; } textarea { display: block; width: 100%; min-height: 74px; box-sizing: border-box; padding: 8px 10px; resize: vertical; border: 1px solid var(--td-component-stroke); border-radius: 5px; background: var(--td-bg-color-container); color: var(--td-text-color-primary); font-family: inherit; font-size: 12px; line-height: 20px; &::placeholder { color: var(--td-text-color-placeholder); } &:focus { border-color: var(--td-brand-color); outline: 2px solid var(--td-brand-color-1); } &:disabled { background: var(--td-bg-color-component-disabled); } } }
.submit-row { display: flex; justify-content: space-between; align-items: center; gap: 20px; margin-top: 20px; padding-top: 16px; border-top: 1px solid var(--td-component-stroke); p { margin: 0; font-size: 12px; line-height: 20px; color: var(--td-text-color-secondary); } .t-button { flex-shrink: 0; } .t-icon { margin-right: 5px; } }
.check-error { display: flex; gap: 8px; align-items: flex-start; margin-top: 16px; padding: 14px 16px; border: 1px solid var(--td-error-color-3); border-radius: 8px; background: var(--td-error-color-1); font-size: 13px; line-height: 22px; overflow-wrap: anywhere; > .t-icon { flex-shrink: 0; margin-top: 2px; color: var(--td-error-color); } }
.loading-state { display: flex; align-items: center; justify-content: center; gap: 12px; padding: 45px 20px; font-size: 13px; strong { font-weight: 500; } p { margin: 5px 0 0; font-size: 12px; color: var(--td-text-color-secondary); } }
.ready-state { display: flex; flex-direction: column; align-items: center; text-align: center; padding: 32px 20px 12px; color: var(--td-text-color-placeholder); > strong { margin-top: 10px; font-size: 13px; font-weight: 500; color: var(--td-text-color-secondary); } p { margin: 5px 0 0; font-size: 12px; line-height: 20px; } }
.check-report { margin-top: 26px; }
.report-heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; h3 { display: flex; align-items: center; flex-wrap: wrap; gap: 12px; font-size: 18px; font-weight: 600; margin: 0; } p { font-size: 12px; color: var(--td-text-color-secondary); margin: 6px 0 0; } }
.report-actions { display: flex; gap: 8px; flex-shrink: 0; .t-icon { margin-right: 4px; } }
.status-chip { display: inline-flex; align-items: center; padding: 3px 8px; border-radius: 4px; font-size: 12px; line-height: 18px; font-weight: 500; }
.status-fail { background: var(--td-error-color-1); color: var(--td-error-color-7); }
.status-review { background: var(--td-warning-color-1); color: var(--td-warning-color-7); }
.status-pass { background: var(--td-success-color-1); color: var(--td-success-color-7); }
.report-files { display: flex; flex-wrap: wrap; gap: 6px 24px; margin: 14px 0; font-size: 12px; line-height: 20px; > div { display: flex; gap: 8px; } dt { color: var(--td-text-color-secondary); flex-shrink: 0; } dd { margin: 0; overflow-wrap: anywhere; } }
.summary-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; margin: 16px 0; }
.summary-stat { display: flex; align-items: center; gap: 12px; padding: 14px 16px; border: 1px solid var(--td-component-stroke); border-radius: 8px; background: var(--td-bg-color-container); strong { font-size: 26px; font-weight: 600; line-height: 30px; } span { font-size: 12px; color: var(--td-text-color-secondary); } }
.stat-fail strong { color: var(--td-error-color); }.stat-review strong { color: var(--td-warning-color); }.stat-pass strong { color: var(--td-success-color); }
.coverage-notice { padding: 14px 16px; border: 1px solid var(--td-component-stroke); border-radius: 8px; background: var(--td-bg-color-secondarycontainer); font-size: 12px; line-height: 21px; strong { font-size: 13px; font-weight: 500; } p { margin: 5px 0 0; color: var(--td-text-color-secondary); } ul { margin: 8px 0 0; padding-left: 18px; color: var(--td-text-color-secondary); } }
.report-filter { display: flex; flex-wrap: wrap; gap: 8px; margin: 20px 0 12px; button { padding: 6px 12px; border: 1px solid var(--td-component-stroke); border-radius: 6px; background: var(--td-bg-color-container); color: var(--td-text-color-secondary); font-size: 12px; font-family: inherit; cursor: pointer; &:focus-visible { outline: 2px solid var(--td-brand-color); outline-offset: 2px; } &.selected { color: var(--td-brand-color); border-color: var(--td-brand-color); background: var(--td-brand-color-1); } } }
.no-checks { padding: 20px; text-align: center; color: var(--td-text-color-secondary); font-size: 13px; }
.check-list { display: flex; flex-direction: column; gap: 12px; }
.check-item { padding: 16px 18px; border: 1px solid var(--td-component-stroke); border-left-width: 3px; border-radius: 8px; background: var(--td-bg-color-container); overflow-wrap: anywhere; &.check-filtered-out { display: none; } &.check-fail { border-left-color: var(--td-error-color); } &.check-review { border-left-color: var(--td-warning-color); } &.check-pass { border-left-color: var(--td-success-color); } }
.check-heading { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; h4 { font-size: 14px; line-height: 23px; font-weight: 600; margin: 0; } }
.check-message { margin: 10px 0 12px; font-size: 13px; line-height: 22px; }
.check-evidence { margin: 0; font-size: 12px; line-height: 21px; > div { display: grid; grid-template-columns: 78px minmax(0, 1fr); gap: 10px; margin-top: 8px; } dt { color: var(--td-text-color-secondary); } dd { margin: 0; white-space: pre-wrap; } blockquote { margin: 0; } }
.evidence-quote { padding: 8px 10px; border-radius: 5px; background: var(--td-bg-color-secondarycontainer); }
.source-caption { display: block; margin-top: 4px; color: var(--td-text-color-secondary); font-size: 11px; }.missing-source { color: var(--td-text-color-secondary); }
.tender-rules { margin-top: 18px; padding: 14px 16px; border: 1px solid var(--td-component-stroke); border-radius: 8px; font-size: 12px; line-height: 21px; summary { font-weight: 500; cursor: pointer; } ol { padding-left: 20px; margin: 14px 0 0; } li { margin-bottom: 12px; overflow-wrap: anywhere; } p { margin: 4px 0; white-space: pre-wrap; } small { font-size: 11px; color: var(--td-text-color-secondary); } }
@media (max-width: 1100px) { .check-options { grid-template-columns: 1fr; gap: 18px; } }
@media (max-width: 760px) { .anonymous-bid-check-page { padding: 20px 16px 28px; } .check-form { padding: 16px; } .document-grid { grid-template-columns: 1fr; } .submit-row { align-items: flex-start; flex-direction: column; gap: 12px; > .t-button { width: 100%; } } .report-heading { align-items: flex-start; flex-direction: column; } .summary-stat { flex-direction: column; gap: 4px; padding: 12px 6px; text-align: center; } .check-evidence > div { grid-template-columns: 1fr; gap: 3px; } }
@media print { .check-form, .page-header > p, .check-error, .report-actions, .report-filter, .no-checks, .ready-state, .loading-state { display: none !important; } .anonymous-bid-check-page { height: auto; overflow: visible; padding: 0; color: #111; } .check-report { margin-top: 18px; } .check-item.check-filtered-out { display: block; } .check-item, .coverage-notice, .summary-stat, .tender-rules { background: white; border-color: #ccc; } .check-item, .summary-grid { break-inside: avoid; } .status-chip { border: 1px solid #ccc; color: #111; background: white; } }
</style>

<style lang="less">
@media print {
  html:has(.anonymous-bid-check-page),
  body:has(.anonymous-bid-check-page),
  body:has(.anonymous-bid-check-page) #app,
  body:has(.anonymous-bid-check-page) .main,
  body:has(.anonymous-bid-check-page) .platform-route-outlet {
    height: auto !important;
    min-height: 0 !important;
    max-height: none !important;
    min-width: 0 !important;
    overflow: visible !important;
    display: block !important;
    background: white !important;
  }
  body:has(.anonymous-bid-check-page) {
    --td-text-color-primary: #111;
    --td-text-color-secondary: #555;
    --td-text-color-placeholder: #666;
    --td-bg-color-container: #fff;
    --td-bg-color-secondarycontainer: #f6f6f6;
    color-scheme: light;
  }
  // App.vue promotes #app to a transformed layer; remove it for paged output.
  body:has(.anonymous-bid-check-page) #app {
    transform: none !important;
    isolation: auto !important;
    backface-visibility: visible !important;
  }
  body:has(.anonymous-bid-check-page) :is(
    .aside_box, .upload-mask, .global-invitation-bell, .upload-tasks-panel,
    .guide, .t-overlay, .t-dialog__ctx, .t-drawer, .t-popup,
    .t-message__list, .t-notification__list
  ) { display: none !important; }
}
</style>
