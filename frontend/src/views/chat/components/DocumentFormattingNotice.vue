<template>
  <span v-if="formatting" class="document-formatting" :class="{ 'has-warning': !!formatting.warning }" role="status">
    <t-tooltip :content="details" placement="top">
      <span class="document-formatting__basis" tabindex="0">
        <t-icon :name="formatting.mode === 'blocked' ? 'info-circle' : 'file-word'" />
        <span>{{ label }}</span>
        <span class="document-formatting__scope">{{ scope }}</span>
      </span>
    </t-tooltip>
    <span v-if="formatting.mode !== 'tender' && checkedSources" class="document-formatting__sources">{{ checkedSources }}</span>
    <span v-if="formatting.warning" class="document-formatting__warning">{{ formatting.warning }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { readDocumentFormatting, documentFormattingLabel, documentFormattingSourceLabel } from '@/utils/documentFormatting'

const props = defineProps<{ value?: unknown }>()
const { t } = useI18n()
const formatting = computed(() => readDocumentFormatting(props.value))
const label = computed(() => formatting.value ? documentFormattingLabel(formatting.value, t) : '')
const scope = computed(() => t(formatting.value?.scope === 'technical'
  ? 'chat.wordFormattingTechnical' : 'chat.wordFormattingBusiness'))
const checkedSources = computed(() => formatting.value ? documentFormattingSourceLabel(formatting.value, t) : '')
const details = computed(() => formatting.value
  ? [label.value, scope.value, checkedSources.value, ...formatting.value.summary, formatting.value.warning].filter(Boolean).join('\n') : '')
</script>

<style scoped lang="less">
.document-formatting {
  display: inline-flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 8px;
  max-width: 100%;
  min-width: 0;
  color: var(--td-text-color-secondary);
  font-size: 12px;
  line-height: 1.6;
  overflow-wrap: anywhere;

  &.has-warning { flex-basis: 100%; }
  &__basis { display: inline-flex; flex-wrap: wrap; align-items: center; gap: 4px; min-width: 0; }
  &__scope { color: var(--td-text-color-placeholder); }
  &__sources { color: var(--td-text-color-secondary); }
  &__warning { color: var(--td-warning-color-7); flex-basis: 100%; white-space: pre-line; }
}
</style>
