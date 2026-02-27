<template>
  <div class="overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
    <div class="flex gap-4 p-4">
      <!-- 左侧：媒体预览 -->
      <div class="relative h-32 w-48 shrink-0 overflow-hidden rounded-md bg-gray-100 dark:bg-dark-700">
        <!-- 已完成 — 显示视频/图片 -->
        <template v-if="generation.status === 'completed' && generation.media_url">
          <video
            v-if="generation.media_type === 'video'"
            :src="generation.media_url"
            class="h-full w-full object-cover"
            muted
            loop
            @mouseenter="($event.target as HTMLVideoElement).play()"
            @mouseleave="($event.target as HTMLVideoElement).pause()"
          />
          <img
            v-else
            :src="generation.media_url"
            class="h-full w-full object-cover"
            alt=""
          />
        </template>
        <!-- 进行中 — 动画 -->
        <div v-else-if="generation.status === 'pending' || generation.status === 'generating'" class="flex h-full w-full items-center justify-center">
          <div class="h-8 w-8 animate-spin rounded-full border-2 border-primary-500 border-t-transparent" />
        </div>
        <!-- 失败/取消 — 图标 -->
        <div v-else class="flex h-full w-full items-center justify-center text-gray-400">
          <svg class="h-8 w-8" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path v-if="generation.status === 'failed'" stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M12 9v3.75m9-.75a9 9 0 11-18 0 9 9 0 0118 0zm-9 3.75h.008v.008H12v-.008z" />
            <path v-else stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M18.364 18.364A9 9 0 005.636 5.636m12.728 12.728A9 9 0 015.636 5.636m12.728 12.728L5.636 5.636" />
          </svg>
        </div>
      </div>

      <!-- 右侧：信息 -->
      <div class="flex min-w-0 flex-1 flex-col justify-between">
        <div>
          <!-- 状态 + 模型 -->
          <div class="mb-1 flex items-center gap-2">
            <span class="inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium" :class="statusClass">
              {{ statusText }}
            </span>
            <span class="text-xs text-gray-500 dark:text-gray-400">{{ generation.model }}</span>
          </div>
          <!-- 提示词 -->
          <p class="line-clamp-2 text-sm text-gray-700 dark:text-gray-300">{{ generation.prompt }}</p>
          <!-- 错误信息 -->
          <p v-if="generation.status === 'failed' && generation.error_message" class="mt-1 text-xs text-red-500">
            {{ generation.error_message }}
          </p>
        </div>

        <!-- Upstream 过期倒计时 -->
        <div
          v-if="generation.status === 'completed' && generation.storage_type === 'upstream'"
          class="mt-2"
        >
          <div class="flex items-center gap-2">
            <div class="h-1.5 flex-1 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-600">
              <div
                class="h-full rounded-full transition-all duration-1000"
                :class="countdownPercent <= 33 ? 'bg-red-500' : countdownPercent <= 66 ? 'bg-amber-500' : 'bg-green-500'"
                :style="{ width: countdownPercent + '%' }"
              />
            </div>
            <span
              class="shrink-0 text-xs font-medium"
              :class="isExpired ? 'text-red-500' : remainingMs <= 5 * 60 * 1000 ? 'text-red-500 dark:text-red-400' : 'text-gray-500 dark:text-gray-400'"
            >
              {{ isExpired ? t('sora.upstreamExpired') : t('sora.upstreamCountdown', { time: countdownText }) }}
            </span>
          </div>
        </div>

        <!-- 操作按钮 -->
        <div class="mt-2 flex items-center gap-2">
          <!-- 进行中：取消 -->
          <button
            v-if="generation.status === 'pending' || generation.status === 'generating'"
            class="rounded px-2.5 py-1 text-xs text-gray-600 transition-colors hover:bg-gray-100 dark:text-gray-400 dark:hover:bg-dark-700"
            @click="emit('cancel', generation.id)"
          >
            {{ t('sora.cancel') }}
          </button>

          <!-- 已完成：保存 / 下载 -->
          <template v-if="generation.status === 'completed'">
            <button
              v-if="generation.storage_type === 'upstream'"
              class="rounded bg-primary-50 px-2.5 py-1 text-xs text-primary-700 transition-colors hover:bg-primary-100 dark:bg-primary-900/30 dark:text-primary-400"
              @click="emit('save', generation.id)"
            >
              {{ t('sora.save') }}
            </button>
            <span v-else class="text-xs text-green-600 dark:text-green-400">{{ t('sora.saved') }}</span>
            <a
              v-if="generation.media_url"
              :href="generation.media_url"
              target="_blank"
              class="rounded px-2.5 py-1 text-xs text-gray-600 transition-colors hover:bg-gray-100 dark:text-gray-400 dark:hover:bg-dark-700"
            >
              {{ t('sora.download') }}
            </a>
          </template>

          <!-- 失败/取消：重试 -->
          <button
            v-if="generation.status === 'failed' || generation.status === 'cancelled'"
            class="rounded bg-primary-50 px-2.5 py-1 text-xs text-primary-700 transition-colors hover:bg-primary-100 dark:bg-primary-900/30 dark:text-primary-400"
            @click="emit('retry', generation)"
          >
            {{ t('sora.retry') }}
          </button>

          <!-- 通用：删除 -->
          <button
            v-if="generation.status !== 'pending' && generation.status !== 'generating'"
            class="rounded px-2.5 py-1 text-xs text-red-500 transition-colors hover:bg-red-50 dark:hover:bg-red-900/20"
            @click="emit('delete', generation.id)"
          >
            {{ t('sora.delete') }}
          </button>

          <!-- 时间 -->
          <span class="ml-auto text-xs text-gray-400 dark:text-gray-500">{{ formatTime(generation.created_at) }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SoraGeneration } from '@/api/sora'

const props = defineProps<{ generation: SoraGeneration }>()
const emit = defineEmits<{
  cancel: [id: number]
  delete: [id: number]
  save: [id: number]
  retry: [gen: SoraGeneration]
}>()
const { t } = useI18n()

const statusClass = computed(() => {
  switch (props.generation.status) {
    case 'pending': return 'bg-yellow-100 text-yellow-800 dark:bg-yellow-900/30 dark:text-yellow-400'
    case 'generating': return 'bg-blue-100 text-blue-800 dark:bg-blue-900/30 dark:text-blue-400'
    case 'completed': return 'bg-green-100 text-green-800 dark:bg-green-900/30 dark:text-green-400'
    case 'failed': return 'bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-400'
    case 'cancelled': return 'bg-gray-100 text-gray-800 dark:bg-gray-700 dark:text-gray-400'
    default: return 'bg-gray-100 text-gray-800'
  }
})

const statusText = computed(() => {
  const key = `sora.status${props.generation.status.charAt(0).toUpperCase() + props.generation.status.slice(1)}`
  return t(key)
})

function formatTime(iso: string): string {
  const d = new Date(iso)
  const now = new Date()
  const diff = now.getTime() - d.getTime()
  if (diff < 60000) return t('sora.justNow')
  if (diff < 3600000) return t('sora.minutesAgo', { n: Math.floor(diff / 60000) })
  if (diff < 86400000) return t('sora.hoursAgo', { n: Math.floor(diff / 3600000) })
  return d.toLocaleDateString()
}

// ==================== Upstream 15 分钟倒计时 ====================

const UPSTREAM_TTL = 15 * 60 * 1000 // 15 分钟（毫秒）
const now = ref(Date.now())
let countdownTimer: ReturnType<typeof setInterval> | null = null

/** 过期截止时间 */
const expireTime = computed(() => {
  if (!props.generation.completed_at) return 0
  return new Date(props.generation.completed_at).getTime() + UPSTREAM_TTL
})

/** 剩余毫秒数 */
const remainingMs = computed(() => Math.max(0, expireTime.value - now.value))

/** 是否已过期 */
const isExpired = computed(() => remainingMs.value <= 0)

/** 进度条百分比 (100% → 0%) */
const countdownPercent = computed(() => {
  if (isExpired.value) return 0
  return Math.round((remainingMs.value / UPSTREAM_TTL) * 100)
})

/** 格式化剩余时间 mm:ss */
const countdownText = computed(() => {
  const totalSec = Math.ceil(remainingMs.value / 1000)
  const m = Math.floor(totalSec / 60)
  const s = totalSec % 60
  return `${m}:${s.toString().padStart(2, '0')}`
})

onMounted(() => {
  // 仅对 upstream 类型的已完成记录启动倒计时
  if (props.generation.status === 'completed' && props.generation.storage_type === 'upstream') {
    countdownTimer = setInterval(() => {
      now.value = Date.now()
      // 过期后停止计时
      if (now.value >= expireTime.value && countdownTimer) {
        clearInterval(countdownTimer)
        countdownTimer = null
      }
    }, 1000)
  }
})

onUnmounted(() => {
  if (countdownTimer) {
    clearInterval(countdownTimer)
    countdownTimer = null
  }
})
</script>
