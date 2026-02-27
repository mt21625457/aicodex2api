<template>
  <Teleport to="body">
    <Transition name="fade">
      <div v-if="visible && generation" class="fixed inset-0 z-50 flex items-center justify-center p-4" @click.self="emit('close')">
        <div class="fixed inset-0 bg-black/50 backdrop-blur-sm" />
        <div class="relative z-10 w-full max-w-md rounded-xl bg-white p-6 shadow-2xl dark:bg-dark-800">
          <!-- 头部 -->
          <div class="mb-4 flex items-center justify-between">
            <h3 class="text-lg font-semibold text-gray-900 dark:text-gray-100">{{ t('sora.downloadTitle') }}</h3>
            <button @click="emit('close')" class="rounded-full p-1 text-gray-400 hover:bg-gray-100 hover:text-gray-600 dark:hover:bg-dark-700">
              <svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" /></svg>
            </button>
          </div>

          <!-- 内容 -->
          <div class="space-y-4">
            <!-- 媒体类型 + 模型信息 -->
            <div class="flex items-center gap-3">
              <div class="flex h-10 w-10 items-center justify-center rounded-full" :class="generation.media_type === 'video' ? 'bg-blue-100 text-blue-600 dark:bg-blue-900/30 dark:text-blue-400' : 'bg-purple-100 text-purple-600 dark:bg-purple-900/30 dark:text-purple-400'">
                <svg v-if="generation.media_type === 'video'" class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 10l4.553-2.276A1 1 0 0121 8.618v6.764a1 1 0 01-1.447.894L15 14M5 18h8a2 2 0 002-2V8a2 2 0 00-2-2H5a2 2 0 00-2 2v8a2 2 0 002 2z" /></svg>
                <svg v-else class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16l4.586-4.586a2 2 0 012.828 0L16 16m-2-2l1.586-1.586a2 2 0 012.828 0L20 14m-6-6h.01M6 20h12a2 2 0 002-2V6a2 2 0 00-2-2H6a2 2 0 00-2 2v12a2 2 0 002 2z" /></svg>
              </div>
              <div>
                <p class="font-medium text-gray-900 dark:text-gray-100">{{ generation.model }}</p>
                <p class="text-xs text-gray-500 dark:text-gray-400">{{ generation.media_type === 'video' ? t('sora.mediaTypeVideo') : t('sora.mediaTypeImage') }}</p>
              </div>
            </div>

            <!-- 倒计时显示 -->
            <div v-if="remainingText" class="flex items-center gap-2 text-sm">
              <svg class="h-4 w-4 text-gray-400" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z" /></svg>
              <span :class="isExpired ? 'text-red-500' : 'text-gray-600 dark:text-gray-300'">
                {{ isExpired ? t('sora.upstreamExpired') : t('sora.upstreamCountdown', { time: remainingText }) }}
              </span>
            </div>

            <!-- 过期警告 -->
            <div class="rounded-lg border border-amber-200 bg-amber-50 p-3 dark:border-amber-800/50 dark:bg-amber-900/20">
              <p class="text-sm text-amber-800 dark:text-amber-200">{{ t('sora.downloadExpirationWarning') }}</p>
            </div>

            <!-- 下载按钮 -->
            <a
              v-if="generation.media_url"
              :href="generation.media_url"
              target="_blank"
              download
              class="flex w-full items-center justify-center gap-2 rounded-lg bg-primary-600 px-4 py-2.5 text-sm font-medium text-white transition-colors hover:bg-primary-700"
            >
              <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4" /></svg>
              {{ t('sora.downloadNow') }}
            </a>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup lang="ts">
import { ref, computed, watch, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SoraGeneration } from '@/api/sora'

const EXPIRATION_MINUTES = 15

const props = defineProps<{
  visible: boolean
  generation: SoraGeneration | null
}>()

const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()

const now = ref(Date.now())
let timer: ReturnType<typeof setInterval> | null = null

/** 过期时间戳 */
const expiresAt = computed(() => {
  if (!props.generation?.completed_at) return null
  return new Date(props.generation.completed_at).getTime() + EXPIRATION_MINUTES * 60 * 1000
})

/** 是否已过期 */
const isExpired = computed(() => {
  if (!expiresAt.value) return false
  return now.value >= expiresAt.value
})

/** 格式化剩余时间 */
const remainingText = computed(() => {
  if (!expiresAt.value) return ''
  const diff = expiresAt.value - now.value
  if (diff <= 0) return ''
  const minutes = Math.floor(diff / 60000)
  const seconds = Math.floor((diff % 60000) / 1000)
  return `${minutes}:${String(seconds).padStart(2, '0')}`
})

/** 弹窗可见时启动倒计时定时器 */
watch(
  () => props.visible,
  (v) => {
    if (v) {
      now.value = Date.now()
      timer = setInterval(() => { now.value = Date.now() }, 1000)
    } else if (timer) {
      clearInterval(timer)
      timer = null
    }
  },
  { immediate: true }
)

onUnmounted(() => {
  if (timer) clearInterval(timer)
})
</script>

<style scoped>
.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.2s ease;
}
.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}
</style>
