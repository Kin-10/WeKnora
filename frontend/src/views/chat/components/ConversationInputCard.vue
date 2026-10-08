<template>
  <section class="conversation-input-card" :class="{ 'is-answered': answered }" :aria-labelledby="`${fieldPrefix}-title`">
    <div class="card-header">
      <h3 :id="`${fieldPrefix}-title`">{{ request.title }}</h3>
      <span v-if="answered" class="answered-label">{{ t('chat.conversationInput.answered') }}</span>
    </div>
    <form v-if="!answered" ref="formElement" novalidate @submit.prevent="submit">
      <p class="card-introduction">{{ t('chat.conversationInput.introduction') }}</p>
      <fieldset
        v-for="(question, questionIndex) in request.questions"
        :key="question.id"
        :disabled="locked"
        :aria-describedby="describedBy(question, questionIndex)"
        :aria-invalid="missing.includes(question.id) ? 'true' : undefined"
        class="question-field"
      >
        <legend>
          <span>{{ question.label }}</span>
          <span class="requirement-label">{{ t(question.required ? 'chat.conversationInput.required' : 'chat.conversationInput.optional') }}</span>
        </legend>
        <p v-if="question.type === 'multiple'" :id="`${fieldPrefix}-${questionIndex}-hint`" class="field-hint">
          {{ t('chat.conversationInput.multipleHint') }}
        </p>
        <template v-if="question.type === 'text'">
          <textarea
            :id="`${fieldPrefix}-${questionIndex}-text`"
            v-model="answers[question.id]"
            :aria-label="question.label"
            :aria-required="question.required"
            :aria-invalid="missing.includes(question.id) ? 'true' : undefined"
            :aria-describedby="describedBy(question, questionIndex)"
            :placeholder="question.placeholder"
            maxlength="5000"
            rows="3"
            @input="clearError(question.id)"
          />
        </template>
        <template v-else>
          <div class="choice-list">
            <label
              v-for="(option, optionIndex) in question.options"
              :key="option.value"
              :for="`${fieldPrefix}-${questionIndex}-${optionIndex}`"
              class="choice-option"
            >
              <input
                :id="`${fieldPrefix}-${questionIndex}-${optionIndex}`"
                v-model="answers[question.id]"
                :type="question.type === 'multiple' ? 'checkbox' : 'radio'"
                :name="`${fieldPrefix}-${questionIndex}`"
                :value="option.value"
                @change="clearError(question.id)"
              />
              <span>{{ option.label }}</span>
            </label>
          </div>
          <label :for="`${fieldPrefix}-${questionIndex}-other`" class="other-label">
            {{ t('chat.conversationInput.otherLabel') }}
          </label>
          <textarea
            :id="`${fieldPrefix}-${questionIndex}-other`"
            v-model="supplements[question.id]"
            :placeholder="question.placeholder || t('chat.conversationInput.otherPlaceholder')"
            maxlength="5000"
            :aria-invalid="missing.includes(question.id) ? 'true' : undefined"
            :aria-describedby="describedBy(question, questionIndex)"
            rows="2"
            @input="clearError(question.id)"
          />
        </template>
        <p v-if="missing.includes(question.id)" :id="`${fieldPrefix}-${questionIndex}-error`" class="field-error" role="alert">
          {{ t('chat.conversationInput.requiredError') }}
        </p>
      </fieldset>
      <div class="card-actions">
        <button type="submit" class="submit-button" :disabled="locked">
          {{ t(disabled ? 'chat.conversationInput.sending' : 'chat.conversationInput.submit') }}
        </button>
      </div>
    </form>
  </section>
</template>

<script lang="ts">
import type { ConversationInputRequest } from '@/utils/conversationInput'

/** Collect only supplied answers; supplemental text can also satisfy a required choice. */
export function collectConversationInputValues(
  request: ConversationInputRequest,
  answers: Record<string, string | string[]>,
  supplements: Record<string, string>,
): { values: Record<string, string | string[]>; missing: string[] } {
  const entries: [string, string | string[]][] = []
  const missing: string[] = []
  for (const question of request.questions) {
    const answer = answers[question.id]
    let value: string | string[] = ''
    if (question.type === 'text') {
      value = typeof answer === 'string' ? answer.trim() : ''
    } else {
      const allowed = new Set(question.options?.map((option) => option.value) || [])
      const selected = question.type === 'multiple'
        ? [...new Set(Array.isArray(answer) ? answer.filter((item) => allowed.has(item)) : [])]
        : typeof answer === 'string' && allowed.has(answer) ? [answer] : []
      const supplement = supplements[question.id]?.trim()
      if (supplement) selected.push(supplement)
      value = question.type === 'single' && selected.length === 1 ? selected[0]! : selected
    }
    const supplied = value.length > 0
    if (supplied) entries.push([question.id, value])
    else if (question.required) missing.push(question.id)
  }
  return { values: Object.fromEntries(entries), missing }
}
</script>

<script setup lang="ts">
import { computed, nextTick, reactive, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'

const props = withDefaults(defineProps<{
  request: ConversationInputRequest
  disabled?: boolean
  answered?: boolean
}>(), { disabled: false, answered: false })
const emit = defineEmits<{ submit: [values: Record<string, string | string[]>] }>()
const { t } = useI18n()
const fieldPrefix = `conversation-input-${useId()}`
const formElement = ref<HTMLFormElement>()
const answers = reactive<Record<string, string | string[]>>(Object.create(null))
const supplements = reactive<Record<string, string>>(Object.create(null))
const missing = ref<string[]>([])
const locked = computed(() => props.disabled || props.answered)

watch(() => props.request.id, () => {
  for (const key of Object.keys(answers)) delete answers[key]
  for (const key of Object.keys(supplements)) delete supplements[key]
  for (const question of props.request.questions) {
    answers[question.id] = question.type === 'multiple' ? [] : ''
    supplements[question.id] = ''
  }
  missing.value = []
}, { immediate: true })

function describedBy(question: ConversationInputRequest['questions'][number], index: number): string | undefined {
  const ids: string[] = []
  if (question.type === 'multiple') ids.push(`${fieldPrefix}-${index}-hint`)
  if (missing.value.includes(question.id)) ids.push(`${fieldPrefix}-${index}-error`)
  return ids.length ? ids.join(' ') : undefined
}

function clearError(id: string) {
  missing.value = missing.value.filter((item) => item !== id)
}

async function submit() {
  if (locked.value) return
  const result = collectConversationInputValues(props.request, answers, supplements)
  missing.value = result.missing
  if (result.missing.length) {
    await nextTick()
    const index = props.request.questions.findIndex((question) => question.id === result.missing[0])
    formElement.value?.querySelectorAll('fieldset')[index]?.querySelector<HTMLElement>('input, textarea')?.focus()
    return
  }
  emit('submit', result.values)
}
</script>

<style scoped lang="less">
.conversation-input-card {
  margin: 16px 0;
  padding: 20px;
  border: 1px solid var(--td-component-stroke, #e5e7eb);
  border-radius: 12px;
  background: var(--td-bg-color-container, #fff);
  color: var(--td-text-color-primary, #1f2937);
  max-width: 100%;
  box-sizing: border-box;
}
.card-header {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 16px;
  align-items: center;
  h3 { margin: 0; font-size: 15px; line-height: 1.6; font-weight: 600; overflow-wrap: anywhere; }
}
.card-introduction, .field-hint, .requirement-label, .answered-label, .other-label {
  color: var(--td-text-color-secondary, #6b7280);
  font-size: 12px;
  line-height: 1.6;
}
.card-introduction { margin: 6px 0 18px; }
.answered-label { margin-left: auto; }
.question-field {
  border: 0;
  padding: 0;
  margin: 0 0 20px;
  min-width: 0;
  legend { padding: 0; margin-bottom: 10px; font-size: 14px; font-weight: 500; line-height: 1.6; overflow-wrap: anywhere; }
  &:disabled { opacity: .65; }
}
.requirement-label { margin-left: 8px; font-weight: 400; white-space: nowrap; }
.field-hint { margin: -6px 0 10px; }
.choice-list { display: flex; flex-direction: column; gap: 8px; }
.choice-option {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  font-size: 14px;
  line-height: 1.6;
  cursor: pointer;
  span { overflow-wrap: anywhere; }
  input { flex: 0 0 auto; margin: 4px 0 0; accent-color: var(--td-brand-color, #07a35a); cursor: inherit; }
}
.other-label { display: block; margin: 12px 0 6px; }
textarea {
  display: block;
  width: 100%;
  padding: 10px 12px;
  box-sizing: border-box;
  min-height: 64px;
  resize: vertical;
  border: 1px solid var(--td-component-border, #d1d5db);
  border-radius: 8px;
  background: var(--td-bg-color-container, #fff);
  color: inherit;
  font: inherit;
  font-size: 14px;
  line-height: 1.6;
  &:focus { outline: 2px solid var(--td-brand-color, #07a35a); outline-offset: 1px; }
  &[aria-invalid='true'] { border-color: var(--td-error-color, #d54941); }
  &::placeholder { color: var(--td-text-color-placeholder, #9ca3af); }
}
.field-error { margin: 6px 0 0; font-size: 12px; color: var(--td-error-color, #d54941); }
.card-actions { display: flex; justify-content: flex-end; }
.submit-button {
  border: 0;
  padding: 9px 16px;
  border-radius: 8px;
  font: inherit;
  font-size: 14px;
  line-height: 1.5;
  background: var(--td-brand-color, #07a35a);
  color: var(--td-text-color-anti, #fff);
  cursor: pointer;
  &:focus-visible { outline: 2px solid var(--td-brand-color, #07a35a); outline-offset: 3px; }
  &:disabled { opacity: .6; cursor: not-allowed; }
}
.is-answered { padding: 14px 20px; }
@media (max-width: 480px) {
  .conversation-input-card { padding: 16px; }
  .card-actions .submit-button { width: 100%; }
}
</style>
