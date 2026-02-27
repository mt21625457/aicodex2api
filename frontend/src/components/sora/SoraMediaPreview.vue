<template>
  <Teleport to="body">
    <Transition name="fade">
      <div
        v-if="visible && generation"
        class="fixed inset-0 z-50 flex items-center justify-center"
        @keydown.esc="emit('close')"
      >
        <!-- 背景遮罩 -->
        <div
          class="absolute inset-0 bg-black/60 backdrop-blur-sm"
          @click="emit('close')"
        />

        <!-- 内容区 -->
        <div class="relative z-10 flex max-h-[90vh] max-w-[90vw] flex-col overflow-hidden rounded-xl bg-white shadow-2xl dark:bg-dark-800">
          <!-- 顶部栏 -->
          <div class="flex items-center justify-between border-b border-gray-200 px-4 py-3 dark:border-dark-700">
            <h3 class="text-sm font-medium text-gray-900 dark:text-gray-100">{{ t('sora.previewTitle') }}</h3>
            <button
              class="rounded-lg p-1.5 text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600 dark:hover:bg-dark-700 dark:hover:text-gray-300"
              @click="emit('close')"
            >
              <svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
              </svg>
            </button>
          </div>

          <!-- 媒体区 -->
          <div class="flex-1 overflow-auto bg-gray-950 p-2">
            <video
              v-if="generation.media_type === 'video'"
              :src="generation.media_url"
              class="max-h-[70vh] w-full rounded object-contain"
              controls
              autoplay
            />
            <img
              v-else
              :src="generation.media_url"
              class="max-h-[70vh] w-full rounded object-contain"
              alt=""
            />
          </div>

          <!-- 详情 + 操作 -->
          <div class="border-t border-gray-200 px-4 py-3 dark:border-dark-700">
            <!-- 模型 + 时间 -->
            <div class="mb-2 flex items-center gap-3 text-xs text-gray-500 dark:text-gray-400">
              <span class="inline-flex items-center rounded-full bg-gray-100 px-2 py-0.5 font-medium dark:bg-dark-700">
                {{ generation.model }}
              </span>
              <span>{{ formatDateTime(generation.created_at) }}</span>
            </div>
            <!-- 提示词 -->
            <p class="mb-3 line-clamp-3 text-sm text-gray-700 dark:text-gray-300">{{ generation.prompt }}</p>
            <!-- 操作按钮 -->
            <div class="flex items-center gap-2">
              <button
                v-if="generation.storage_type === 'upstream'"
                class="rounded-lg bg-primary-600 px-3 py-1.5 text-xs font-medium text-white transition-colors hover:bg-primary-700"
                @click="emit('save', generation.id)"
              >
                {{ t('sora.save') }}
              </button>
              <a
                v-if="generation.media_url"
                :href="generation.media_url"
                target="_blank"
                download
                class="rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-700 transition-colors hover:bg-gray-50 dark:border-dark-600 dark:bg-dark-700 dark:text-gray-300 dark:hover:bg-dark-600"
                @click="emit('download', generation.media_url)"
              >
                {{ t('sora.download') }}
              </a>
              <button
                class="ml-auto rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-xs font-medium text-gray-700 transition-colors hover:bg-gray-50 dark:border-dark-600 dark:bg-dark-700 dark:text-gray-300 dark:hover:bg-dark-600"
                @click="emit('close')"
              >
                {{ t('sora.closePreview') }}
              </button>
            </div>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SoraGeneration } from '@/api/sora'

defineProps<{
  visible: boolean
  generation: SoraGeneration | null
}>()

const emit = defineEmits<{
  close: []
  save: [id: number]
  download: [url: string]
}>()

const { t } = useI18n()

function formatDateTime(iso: string): string {
  const d = new Date(iso)
  return d.toLocaleString()
}

/** 按 Escape 关闭 */
function handleKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    emit('close')
  }
}

onMounted(() => {
  document.addEventListener('keydown', handleKeydown)
})

onUnmounted(() => {
  document.removeEventListener('keydown', handleKeydown)
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
