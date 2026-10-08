<template>
  <section class="bid-generation-progress" :aria-label="t('chat.bidGeneration.title')" aria-live="polite">
    <div class="progress-heading">
      <h3>{{ task.title || t('chat.bidGeneration.title') }}</h3>
      <span class="task-status">{{ t(`chat.bidGeneration.status.${task.status}`) }}</span>
    </div>
    <p class="progress-description">{{ description }}</p>
    <div v-if="task.sections.length" class="section-summary">
      <progress :value="completedSections" :max="task.sections.length" :aria-label="t('chat.bidGeneration.sectionProgress')" />
      <span>{{ t('chat.bidGeneration.summary', { completed: completedSections, total: task.sections.length, words: task.total_words }) }}</span>
    </div>
    <ol v-if="task.sections.length" class="section-list">
      <li v-for="section in task.sections" :key="section.id" :class="`is-${section.status}`">
        <span class="section-title">{{ section.title }}</span>
        <span class="section-status">{{ t(`chat.bidGeneration.sectionStatus.${section.status}`) }}<template v-if="section.word_count"> · {{ t('chat.bidGeneration.words', { count: section.word_count }) }}</template></span>
      </li>
    </ol>
    <div v-if="$slots.supplement" class="progress-supplement" :data-section-id="task.sections[task.current_section]?.id">
      <slot name="supplement" />
    </div>
    <p v-if="task.error" class="task-error" role="alert">{{ task.error }}</p>
    <div class="progress-actions">
      <button v-if="task.status === 'awaiting_outline'" type="button" class="primary-action" :disabled="busy" @click="emit('confirm')">{{ t('chat.bidGeneration.confirmOutline') }}</button>
      <button v-if="writing" type="button" :disabled="busy" @click="emit('control', 'pause')">{{ t('chat.bidGeneration.pause') }}</button>
      <button v-if="task.status === 'paused' || task.status === 'failed'" type="button" class="primary-action" :disabled="busy" @click="emit('control', 'resume')">{{ t(task.status === 'failed' ? 'chat.bidGeneration.retry' : 'chat.bidGeneration.resume') }}</button>
      <button v-if="active || task.status === 'failed'" type="button" :disabled="busy" @click="emit('control', 'cancel')">{{ t('chat.bidGeneration.cancel') }}</button>
      <button v-if="task.status === 'completed' && task.artifact_message_id && Number.isInteger(task.artifact_index)" type="button" class="primary-action" :disabled="busy" @click="emit('download')">{{ t('chat.bidGeneration.download') }}</button>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { BidGenerationTask } from '@/api/bid-generation'
import { bidGenerationIsActive, bidGenerationIsWriting } from '../bidGeneration'

const props = withDefaults(defineProps<{ task: BidGenerationTask; busy?: boolean }>(), { busy: false })
const emit = defineEmits<{
  confirm: []
  control: [action: 'pause' | 'resume' | 'cancel']
  download: []
}>()
const { t } = useI18n()
const active = computed(() => bidGenerationIsActive(props.task))
const writing = computed(() => bidGenerationIsWriting(props.task))
const completedSections = computed(() => props.task.sections.filter(section => section.status === 'completed').length)
const description = computed(() => {
  if (props.task.status === 'running') {
    if (props.task.phase === 'exporting') return t('chat.bidGeneration.exporting')
    const section = props.task.sections[props.task.current_section]
    return section ? t('chat.bidGeneration.writingSection', { title: section.title }) : t('chat.bidGeneration.writing')
  }
  return t(`chat.bidGeneration.description.${props.task.status}`)
})
</script>

<style scoped lang="less">
.bid-generation-progress { margin: 20px 0; padding: 20px; border: 1px solid var(--td-component-stroke, #e5e7eb); border-radius: 12px; background: var(--td-bg-color-container, #fff); color: var(--td-text-color-primary, #1f2937); }
.progress-heading { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 16px; h3 { font-size: 15px; line-height: 1.6; margin: 0; overflow-wrap: anywhere; } }
.task-status { margin-left: auto; font-size: 12px; color: var(--td-brand-color, #07a35a); }
.progress-description { font-size: 13px; line-height: 1.7; color: var(--td-text-color-secondary, #6b7280); margin: 8px 0 12px; }
.section-summary { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 12px; font-size: 12px; color: var(--td-text-color-secondary, #6b7280); progress { flex: 1; min-width: 120px; height: 8px; accent-color: var(--td-brand-color, #07a35a); } }
.section-list { padding-left: 22px; margin: 16px 0; li { padding: 5px 0; font-size: 13px; line-height: 1.6; } .section-title { overflow-wrap: anywhere; } .section-status { font-size: 12px; color: var(--td-text-color-secondary, #6b7280); margin-left: 10px; } .is-writing .section-title { font-weight: 600; color: var(--td-brand-color, #07a35a); } }
.task-error { font-size: 13px; line-height: 1.6; color: var(--td-error-color, #d54941); overflow-wrap: anywhere; }
.progress-supplement { margin: 16px 0; :deep(.conversation-input-card) { margin: 0; } }
.progress-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; button { font: inherit; font-size: 13px; line-height: 1.5; border: 1px solid var(--td-component-border, #d1d5db); border-radius: 8px; padding: 7px 12px; background: var(--td-bg-color-container, #fff); color: inherit; cursor: pointer; &:disabled { opacity: .55; cursor: not-allowed; } &:focus-visible { outline: 2px solid var(--td-brand-color, #07a35a); outline-offset: 2px; } } .primary-action { background: var(--td-brand-color, #07a35a); border-color: transparent; color: var(--td-text-color-anti, #fff); } }
@media (max-width: 480px) { .bid-generation-progress { padding: 16px; } .section-status { display: block; margin-left: 0 !important; } }
</style>
