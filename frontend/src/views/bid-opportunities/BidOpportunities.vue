<template>
  <main class="bid-opportunities-page">
    <header class="page-header" style="--wails-draggable: drag">
      <h2><t-icon name="search" size="24px" />标讯商机</h2>
      <p>搜索公开标讯，发现招标、采购意向与中标信息。</p>
    </header>

    <section class="search-panel" aria-label="标讯搜索条件">
      <form @submit.prevent="search">
        <div class="keyword-row">
          <t-input v-model="filters.keyword" placeholder="输入产品、项目或单位关键词，如 IVD试剂" aria-label="标讯关键词" :maxlength="200" clearable>
            <template #prefix-icon><t-icon name="search" size="16px" /></template>
          </t-input>
          <t-button theme="primary" type="submit" :loading="pager.loading">搜索标讯</t-button>
        </div>
        <div class="filter-grid">
          <label class="filter-field"><span>标讯类型</span><t-select v-model="filters.bidTypes" :options="typeOptions" multiple clearable placeholder="全部标讯" :min-collapsed-num="1" aria-label="标讯类型" /></label>
          <label class="filter-field"><span>发布时间</span><t-select v-model="filters.timeInterval" :options="TIME_OPTIONS" aria-label="发布时间范围" @change="clearCustomDates" /></label>
          <label class="filter-field"><span>省级地区</span><t-input v-model="filters.province" clearable placeholder="如河北省，多个用逗号分隔" aria-label="省级地区" /></label>
          <label class="filter-field"><span>城市</span><t-input v-model="filters.city" clearable placeholder="如石家庄市" aria-label="城市" /></label>
          <template v-if="advanced">
            <label class="filter-field"><span>区县</span><t-input v-model="filters.county" clearable placeholder="填写完整区县名称" aria-label="区县" /></label>
            <label class="filter-field"><span>匹配方式</span><t-select v-model="filters.matchType" :options="matchOptions" aria-label="关键词匹配方式" /></label>
            <label class="filter-field"><span>排序方式</span><t-select v-model="filters.orderBy" :options="sortOptions" aria-label="排序方式" /></label>
            <label class="filter-field"><span>自定义日期</span><t-date-range-picker v-model="filters.dateRange" format="YYYY-MM-DD" value-type="YYYY-MM-DD" clearable aria-label="自定义发布时间" @change="useCustomDates" /></label>
          </template>
        </div>
        <div class="filter-actions">
          <button type="button" class="advanced-button" :aria-expanded="advanced" @click="advanced = !advanced">{{ advanced ? '收起筛选' : '更多筛选' }}<t-icon :name="advanced ? 'chevron-up' : 'chevron-down'" size="14px" /></button>
          <t-button variant="text" theme="default" size="small" @click="resetFilters">重置</t-button>
        </div>
      </form>
    </section>

    <section ref="resultsRoot" class="results-main" aria-label="标讯搜索结果" :aria-busy="pager.loading">
      <div v-if="pager.error" class="search-error" role="alert">
        <div><strong>{{ pager.error.message }}</strong><span v-if="pager.error.traceId" class="trace-id">追踪编号：{{ pager.error.traceId }}</span></div>
        <t-button variant="outline" size="small" :loading="pager.loading" @click="pager.retry()">重试</t-button>
      </div>
      <div v-if="pager.loading && !pager.items.length" class="loading-state" role="status"><t-loading size="small" /><span>正在搜索标讯…</span></div>
      <EmptyState v-else-if="!pager.searched" icon="search" title="查找适合你的标讯商机" description="输入关键词或选择地区与时间，点击“搜索标讯”开始。" />
      <EmptyState v-else-if="!pager.items.length && !pager.error" icon="search" title="暂无匹配标讯" description="尝试调整关键词、地区或时间范围。">
        <t-button v-if="filters.keyword.trim() && filters.matchType === 'term'" variant="outline" @click="fuzzySearch">尝试模糊匹配</t-button>
      </EmptyState>
      <template v-else-if="pager.items.length">
        <div class="result-heading" role="status"><span>{{ pager.total === null ? `已加载 ${pager.loadedCount} 条标讯` : `匹配 ${pager.total} 条标讯` }}</span><span>第 {{ pager.currentPage }} 页</span></div>
        <div class="bid-results"><BidOpportunityCard v-for="item in pager.items" :key="opportunityKey(item)" :item="item" @open="openSummary" /></div>
        <div class="page-controls">
          <t-button variant="outline" theme="default" :disabled="!pager.hasPrevious || pager.loading" @click="previousPage">上一页</t-button>
          <span>第 {{ pager.currentPage }} 页</span>
          <t-button variant="outline" theme="default" :disabled="!pager.hasNext || pager.loading" :loading="pager.loading" @click="nextPage">下一页</t-button>
        </div>
        <p v-if="pager.hitLimit" class="page-note">本次已加载 {{ MAX_SEARCH_PAGES }} 页，请缩小筛选范围查看其它标讯。</p>
        <p v-else-if="!pager.hasNext && !pager.loading" class="page-note">{{ pager.stopReason === 'duplicates' ? '下一页未返回新的标讯，已停止翻页。请调整筛选后重新搜索。' : '当前分页已无更多可用结果。' }}</p>
      </template>
    </section>

    <t-drawer v-model:visible="summaryVisible" header="搜索摘要" placement="right" size="min(620px, 92vw)" attach="body" :footer="false" :close-on-overlay-click="true" :close-on-esc-keydown="true" @closed="selected = null">
      <div v-if="selected" class="summary-detail">
        <div class="summary-kind"><span>{{ bidText(selected.bidType) }}</span><span>{{ bidLocation(selected) }}</span></div>
        <h3>{{ bidText(selected.title) || '未提供标题' }}</h3>
        <p class="summary-notice">以下为搜索接口提供的摘要。公告要求、日期和金额请以原始公告为准。</p>
        <dl class="detail-facts">
          <div v-if="bidText(selected.publishDate || selected.publishTime)"><dt>发布时间</dt><dd>{{ bidText(selected.publishDate || selected.publishTime) }}</dd></div>
          <div v-if="unitNames(selected.bidUnit)"><dt>采购单位</dt><dd>{{ unitNames(selected.bidUnit) }}</dd></div>
          <div v-if="unitNames(selected.winBidUnit)"><dt>中标单位</dt><dd>{{ unitNames(selected.winBidUnit) }}</dd></div>
          <div v-if="bidText(selected.budgetAmount)"><dt>预算金额</dt><dd>{{ bidText(selected.budgetAmount) }}</dd></div>
          <div v-if="bidText(selected.winBidAmount)"><dt>中标金额</dt><dd>{{ bidText(selected.winBidAmount) }}</dd></div>
          <div v-if="bidText(selected.bidEndDate)"><dt>投标截止</dt><dd>{{ bidText(selected.bidEndDate) }}</dd></div>
          <div v-if="bidText(selected.tenderProducts)"><dt>标的产品</dt><dd>{{ bidText(selected.tenderProducts) }}</dd></div>
        </dl>
        <section class="summary-body"><h4>搜索摘要</h4><p>{{ bidText(selected.mainBody) || '该条标讯未提供摘要内容。' }}</p></section>
        <a v-if="sourceLink(selected.source)" class="source-link" :href="sourceLink(selected.source)" target="_blank" rel="noopener noreferrer">查看原始公告<t-icon name="jump" size="14px" /></a>
        <p v-else class="source-unavailable">该条标讯未提供可访问的原文链接。</p>
      </div>
    </t-drawer>
  </main>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { searchBidOpportunities, type BidOpportunity } from '@/api/bid-opportunities'
import EmptyState from '@/components/EmptyState.vue'
import BidOpportunityCard from './BidOpportunityCard.vue'
import { BID_TYPES, TIME_OPTIONS, MAX_SEARCH_PAGES, BidSearchPager, bidLocation, bidText, defaultBidFilters, opportunityKey, sourceLink, unitNames } from './search'

const filters = reactive(defaultBidFilters())
const pager = reactive(new BidSearchPager(searchBidOpportunities))
const advanced = ref(false)
const selected = ref<BidOpportunity | null>(null)
const summaryVisible = ref(false)
const resultsRoot = ref<HTMLElement | null>(null)
const typeOptions = BID_TYPES.map(value => ({ label: value, value }))
const matchOptions = [{ label: '精确匹配', value: 'term' }, { label: '模糊匹配', value: 'fuzzy' }]
const sortOptions = [{ label: '发布时间倒序', value: 'PUBLISH_DATE_DESC' }, { label: '相关度', value: 'RELEVANCE' }, { label: '默认排序', value: '' }]

// Invalidate the cursor immediately; old requests cannot repopulate changed filters.
watch(filters, () => { pager.reset() }, { deep: true, flush: 'sync' })
onBeforeUnmount(() => pager.reset())
const search = async () => { await pager.search(filters); await nextTick(); resultsRoot.value?.scrollTo({ top: 0 }) }
const resetFilters = () => { Object.assign(filters, defaultBidFilters()) }
const clearCustomDates = () => { if (filters.dateRange.length) filters.dateRange = [] }
const useCustomDates = () => { if (filters.dateRange.length === 2) filters.timeInterval = 0 }
const fuzzySearch = async () => { filters.matchType = 'fuzzy'; await search() }
const previousPage = () => { pager.previous(); resultsRoot.value?.scrollTo({ top: 0 }) }
const nextPage = async () => { await pager.next(); await nextTick(); resultsRoot.value?.scrollTo({ top: 0 }) }
const openSummary = (item: BidOpportunity) => { selected.value = item; summaryVisible.value = true }
</script>

<style scoped lang="less">
.bid-opportunities-page { flex: 1; min-width: 0; height: 100%; box-sizing: border-box; display: flex; flex-direction: column; margin-right: 16px; padding: 20px 28px 0; color: var(--td-text-color-primary); }
.page-header { flex-shrink: 0; margin-bottom: 18px;
  h2 { display: flex; align-items: center; gap: 8px; margin: 0; font-size: var(--app-text-4xl); font-weight: 600; line-height: 32px; }
  p { margin: 4px 0 0; color: var(--td-text-color-secondary); font-size: 13px; line-height: 20px; }
}
.search-panel { flex-shrink: 0; padding: 18px 20px 10px; border: 1px solid var(--td-component-stroke); border-radius: 10px; background: var(--td-bg-color-container); }
.keyword-row { display: flex; align-items: center; gap: 12px; margin-bottom: 16px;
  > .t-input__wrap { flex: 1; min-width: 0; }
  > .t-button { flex-shrink: 0; }
}
.filter-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px 20px; }
.filter-field { display: flex; align-items: center; gap: 10px; min-width: 0; font-size: 12px;
  > span { flex: 0 0 60px; color: var(--td-text-color-secondary); }
  :deep(.t-input__wrap), :deep(.t-select__wrap), :deep(.t-date-range-picker) { width: 100%; min-width: 0; }
}
.filter-actions { display: flex; align-items: center; justify-content: space-between; margin-top: 8px; }
.advanced-button { appearance: none; display: flex; align-items: center; gap: 3px; padding: 4px 0; border: 0; background: none; color: var(--td-text-color-secondary); font-size: 12px; cursor: pointer; &:hover { color: var(--td-brand-color); } }
.results-main { flex: 1; min-height: 0; overflow-y: auto; padding-bottom: 28px; margin-top: 16px; }
.result-heading { display: flex; justify-content: space-between; margin: 0 2px 12px; font-size: 12px; color: var(--td-text-color-secondary); }
.bid-results { display: flex; flex-direction: column; gap: 12px; }
.loading-state { display: flex; align-items: center; justify-content: center; gap: 10px; padding: 72px 20px; color: var(--td-text-color-secondary); }
.search-error { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 14px 16px; margin-bottom: 12px; border: 1px solid var(--td-error-color-3); border-radius: 8px; background: var(--td-error-color-1); color: var(--td-text-color-primary); font-size: 13px;
  strong { font-weight: 500; word-break: break-word; }
  .t-button { flex-shrink: 0; }
}
.trace-id { display: block; margin-top: 4px; font-size: 11px; color: var(--td-text-color-secondary); word-break: break-all; }
.page-controls { display: flex; align-items: center; justify-content: center; gap: 16px; margin-top: 24px; font-size: 13px; color: var(--td-text-color-secondary); }
.page-note { text-align: center; font-size: 12px; color: var(--td-text-color-placeholder); margin: 12px 0 0; }
.summary-detail { color: var(--td-text-color-primary);
  h3 { margin: 12px 0; font-size: 20px; line-height: 30px; word-break: break-word; }
}
.summary-kind { display: flex; flex-wrap: wrap; gap: 12px; font-size: 12px; color: var(--td-text-color-secondary); }
.summary-notice { padding: 12px 14px; border-radius: 6px; background: var(--td-bg-color-secondarycontainer); color: var(--td-text-color-secondary); font-size: 12px; line-height: 20px; }
.detail-facts { margin: 20px 0;
  > div { display: flex; gap: 16px; margin-top: 10px; font-size: 13px; line-height: 22px; }
  dt { flex: 0 0 60px; color: var(--td-text-color-secondary); }
  dd { margin: 0; word-break: break-word; }
}
.summary-body { padding-top: 16px; border-top: 1px solid var(--td-component-stroke);
  h4 { margin: 0 0 10px; font-size: 14px; font-weight: 600; }
  p { white-space: pre-line; word-break: break-word; font-size: 14px; line-height: 25px; }
}
.source-link { display: inline-flex; align-items: center; gap: 6px; margin-top: 16px; font-size: 13px; color: var(--td-brand-color); text-decoration: none; }
.source-unavailable { margin-top: 20px; color: var(--td-text-color-placeholder); font-size: 12px; }
@media (max-width: 1000px) { .filter-field { flex-direction: column; align-items: stretch; gap: 5px; > span { flex: initial; } } }
@media (max-width: 720px) { .bid-opportunities-page { padding: 20px 16px 0; margin-right: 8px; } .search-panel { padding: 16px 14px 8px; } .keyword-row { gap: 8px; } .filter-grid { gap: 12px; } }
@media (max-width: 480px) { .filter-grid { grid-template-columns: 1fr; } .keyword-row { flex-wrap: wrap; > .t-button { width: 100%; } } }
</style>
