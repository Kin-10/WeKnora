<template>
  <span class="brand-logo" role="img" :aria-label="APP_BRAND">
    <img class="brand-logo__mark" src="/wenshu-icon.svg" alt="" aria-hidden="true" />
    <span class="brand-logo__name" aria-hidden="true">{{ APP_BRAND }}</span>
  </span>
</template>

<script setup lang="ts">
import { APP_BRAND } from '@/config/brand'
</script>

<style scoped>
.brand-logo {
  display: inline-flex;
  align-items: center;
  gap: var(--brand-gap, 6px);
  max-width: 100%;
  color: var(--brand-color, var(--td-text-color-primary));
  white-space: nowrap;
  user-select: none;
}

.brand-logo__mark {
  display: block;
  width: var(--brand-mark-size, 24px);
  height: var(--brand-mark-size, 24px);
  flex-shrink: 0;
}

.brand-logo__name {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Microsoft YaHei', sans-serif;
  font-size: var(--brand-font-size, 14px);
  font-weight: 700;
  line-height: 1.2;
  letter-spacing: -0.3px;
}
</style>
