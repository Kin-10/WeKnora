<template>
  <article class="bid-card">
    <div class="card-meta">
      <span v-if="bidText(item.bidType)" class="bid-type">{{ bidText(item.bidType) }}</span>
      <span v-if="bidText(item.subBidType)">{{ bidText(item.subBidType) }}</span>
      <span v-if="bidLocation(item)">{{ bidLocation(item) }}</span>
      <time v-if="bidText(item.publishDate || item.publishTime)">{{ bidText(item.publishDate || item.publishTime) }}</time>
    </div>
    <h3><button type="button" class="card-title" @click="$emit('open', item)">{{ bidText(item.title) || '未提供标题' }}</button></h3>
    <p v-if="bidText(item.mainBody || item.tenderProducts)" class="card-summary">{{ bidText(item.mainBody || item.tenderProducts) }}</p>
    <dl class="card-facts">
      <div v-if="unitNames(item.bidUnit)"><dt>采购单位</dt><dd>{{ unitNames(item.bidUnit) }}</dd></div>
      <div v-if="unitNames(item.winBidUnit)"><dt>中标单位</dt><dd>{{ unitNames(item.winBidUnit) }}</dd></div>
      <div v-if="bidText(item.budgetAmount)"><dt>预算金额</dt><dd class="amount">{{ bidText(item.budgetAmount) }}</dd></div>
      <div v-if="bidText(item.winBidAmount)"><dt>中标金额</dt><dd class="amount">{{ bidText(item.winBidAmount) }}</dd></div>
      <div v-if="bidText(item.bidEndDate)"><dt>投标截止</dt><dd>{{ bidText(item.bidEndDate) }}<span v-if="typeof item.bidRemainingDays === 'number'" class="deadline-days">{{ remainingLabel(item.bidRemainingDays) }}</span></dd></div>
    </dl>
    <div class="card-footer">
      <div class="tags"><span v-for="(tag, index) in visibleTags" :key="index">{{ tag }}</span></div>
      <button type="button" class="summary-action" @click="$emit('open', item)">查看搜索摘要 <t-icon name="chevron-right" size="14px" /></button>
    </div>
  </article>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { BidOpportunity } from '../../api/bid-opportunities'
import { bidLocation, bidText, unitNames } from './search'
const props = defineProps<{ item: BidOpportunity }>()
defineEmits<{ open: [item: BidOpportunity] }>()
const visibleTags = computed(() => (props.item.tags ?? []).map(tag => bidText(tag.name)).filter(Boolean).slice(0, 6))
const remainingLabel = (days: number) => days < 0 ? ` · 已截止 ${Math.abs(days)} 天` : days === 0 ? ' · 今日截止' : ` · 剩余 ${days} 天`
</script>

<style scoped lang="less">
.bid-card { padding: 20px 22px; border: 1px solid var(--td-component-stroke); border-radius: 10px; background: var(--td-bg-color-container); }
.card-meta { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; color: var(--td-text-color-secondary); font-size: 12px; line-height: 22px; }
.bid-type { color: var(--td-brand-color); background: var(--td-brand-color-light); border-radius: 4px; padding: 0 7px; }
.card-meta time { margin-left: auto; }
h3 { margin: 10px 0 8px; font-size: 16px; line-height: 25px; font-weight: 600; }
.card-title { appearance: none; border: 0; padding: 0; background: none; color: var(--td-text-color-primary); font: inherit; text-align: left; cursor: pointer; word-break: break-word;
  &:hover { color: var(--td-brand-color); }
  &:focus-visible { outline: 2px solid var(--td-brand-color); outline-offset: 4px; border-radius: 2px; }
}
.card-summary { margin: 0 0 14px; color: var(--td-text-color-secondary); font-size: 13px; line-height: 21px; white-space: pre-line; word-break: break-word; display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; }
.card-facts { display: flex; flex-wrap: wrap; gap: 8px 28px; margin: 0; font-size: 13px; line-height: 21px;
  > div { display: flex; gap: 10px; max-width: 100%; }
  dt { flex-shrink: 0; color: var(--td-text-color-placeholder); }
  dd { margin: 0; color: var(--td-text-color-primary); word-break: break-word; }
  .amount { font-weight: 500; }
}
.deadline-days { color: var(--td-text-color-secondary); }
.card-footer { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-top: 14px; }
.tags { display: flex; flex-wrap: wrap; gap: 6px; min-width: 0;
  span { font-size: 11px; color: var(--td-text-color-secondary); background: var(--td-bg-color-secondarycontainer); border-radius: 4px; padding: 2px 6px; }
}
.summary-action { flex-shrink: 0; display: flex; align-items: center; gap: 2px; appearance: none; border: 0; background: none; padding: 2px 0; font-size: 12px; color: var(--td-brand-color); cursor: pointer; }
@media (max-width: 720px) { .bid-card { padding: 16px; } .card-meta time { margin-left: 0; } .card-footer { flex-wrap: wrap; } }
</style>
