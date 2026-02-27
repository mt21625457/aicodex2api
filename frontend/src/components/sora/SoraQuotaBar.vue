<template>
  <div v-if="quota && quota.source !== 'none'" class="flex items-center space-x-3 text-sm">
    <span class="text-gray-500 dark:text-gray-400">{{ t('sora.storage') }}</span>
    <div class="h-2 w-32 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-700">
      <div
        class="h-full rounded-full transition-all"
        :class="percentage > 90 ? 'bg-red-500' : percentage > 70 ? 'bg-yellow-500' : 'bg-primary-500'"
        :style="{ width: `${Math.min(percentage, 100)}%` }"
      />
    </div>
    <span class="whitespace-nowrap text-gray-600 dark:text-gray-300">
      {{ formatBytes(quota.used_bytes) }} / {{ quota.quota_bytes === 0 ? '∞' : formatBytes(quota.quota_bytes) }}
    </span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { QuotaInfo } from '@/api/sora'

const props = defineProps<{ quota: QuotaInfo }>()
const { t } = useI18n()

const percentage = computed(() => {
  if (!props.quota || props.quota.quota_bytes === 0) return 0
  return (props.quota.used_bytes / props.quota.quota_bytes) * 100
})

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return `${(bytes / Math.pow(1024, i)).toFixed(1)} ${units[i]}`
}
</script>
