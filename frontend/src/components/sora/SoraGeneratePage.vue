<template>
  <div class="space-y-4">
    <!-- 进行中的任务列表 -->
    <div v-if="activeGenerations.length > 0" class="space-y-3">
      <SoraProgressCard
        v-for="gen in activeGenerations"
        :key="gen.id"
        :generation="gen"
        @cancel="handleCancel"
        @delete="handleDelete"
        @save="handleSave"
        @retry="handleRetry"
      />
    </div>

    <!-- 空状态 -->
    <div v-else class="flex flex-col items-center justify-center py-16 text-center">
      <svg class="mb-4 h-12 w-12 text-gray-300 dark:text-gray-600" fill="none" viewBox="0 0 24 24" stroke="currentColor">
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M15.362 5.214A8.252 8.252 0 0112 21 8.25 8.25 0 016.038 7.047 8.287 8.287 0 009 9.601a8.983 8.983 0 013.361-6.867 8.21 8.21 0 003 2.48z" />
      </svg>
      <p class="text-gray-500 dark:text-gray-400">{{ t('sora.noActiveGenerations') }}</p>
      <p class="mt-1 text-xs text-gray-400 dark:text-gray-500">{{ t('sora.startGenerating') }}</p>
    </div>

    <!-- 底部创作栏 -->
    <SoraPromptBar @generate="handleGenerate" :generating="generating" />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import soraAPI, { type SoraGeneration, type GenerateRequest } from '@/api/sora'
import SoraProgressCard from './SoraProgressCard.vue'
import SoraPromptBar from './SoraPromptBar.vue'

const { t } = useI18n()
const activeGenerations = ref<SoraGeneration[]>([])
const generating = ref(false)
let pollTimers: Record<number, ReturnType<typeof setTimeout>> = {}

// ==================== 浏览器通知 (Task 10.11) ====================

/** 请求浏览器通知权限 */
function requestNotificationPermission() {
  if ('Notification' in window && Notification.permission === 'default') {
    Notification.requestPermission()
  }
}

/** 发送桌面通知 */
function sendNotification(title: string, body: string) {
  if ('Notification' in window && Notification.permission === 'granted') {
    new Notification(title, { body, icon: '/favicon.ico' })
  }
}

// 标签页标题闪烁
const originalTitle = document.title
let titleBlinkTimer: ReturnType<typeof setInterval> | null = null

function startTitleBlink(message: string) {
  stopTitleBlink()
  let show = true
  titleBlinkTimer = setInterval(() => {
    document.title = show ? message : originalTitle
    show = !show
  }, 1000)
  // 当用户回到当前标签页时停止闪烁
  const onFocus = () => {
    stopTitleBlink()
    window.removeEventListener('focus', onFocus)
  }
  window.addEventListener('focus', onFocus)
}

function stopTitleBlink() {
  if (titleBlinkTimer) {
    clearInterval(titleBlinkTimer)
    titleBlinkTimer = null
  }
  document.title = originalTitle
}

/** 检测任务状态变更并触发通知 */
function checkStatusTransition(oldGen: SoraGeneration, newGen: SoraGeneration) {
  const wasActive = oldGen.status === 'pending' || oldGen.status === 'generating'
  if (!wasActive) return

  if (newGen.status === 'completed') {
    const title = t('sora.notificationCompleted')
    const body = t('sora.notificationCompletedBody', { model: newGen.model })
    sendNotification(title, body)
    if (document.hidden) startTitleBlink(title)
  } else if (newGen.status === 'failed') {
    const title = t('sora.notificationFailed')
    const body = t('sora.notificationFailedBody', { model: newGen.model })
    sendNotification(title, body)
    if (document.hidden) startTitleBlink(title)
  }
}

// ==================== beforeunload 警告 (Task 10.12) ====================

/** 是否存在 upstream 类型的已完成记录 */
const hasUpstreamRecords = computed(() =>
  activeGenerations.value.some(g => g.status === 'completed' && g.storage_type === 'upstream')
)

function beforeUnloadHandler(e: BeforeUnloadEvent) {
  if (hasUpstreamRecords.value) {
    e.preventDefault()
    // 现代浏览器会忽略自定义消息，但仍需赋值以兼容旧版
    e.returnValue = t('sora.beforeUnloadWarning')
    return e.returnValue
  }
}

function getPollingIntervalByRuntime(createdAt: string): number {
  const createdAtMs = new Date(createdAt).getTime()
  if (Number.isNaN(createdAtMs)) {
    return 3000
  }
  const elapsedMs = Date.now() - createdAtMs
  if (elapsedMs < 2 * 60 * 1000) {
    return 3000
  }
  if (elapsedMs < 10 * 60 * 1000) {
    return 10000
  }
  return 30000
}

function schedulePolling(id: number) {
  const current = activeGenerations.value.find(g => g.id === id)
  const interval = current ? getPollingIntervalByRuntime(current.created_at) : 3000
  if (pollTimers[id]) {
    clearTimeout(pollTimers[id])
  }
  pollTimers[id] = setTimeout(() => {
    void pollGeneration(id)
  }, interval)
}

async function pollGeneration(id: number) {
  try {
    const gen = await soraAPI.getGeneration(id)
    const idx = activeGenerations.value.findIndex(g => g.id === id)
    if (idx >= 0) {
      // 检测状态变更并发送通知
      checkStatusTransition(activeGenerations.value[idx], gen)
      activeGenerations.value[idx] = gen
    }
    // 按任务运行时长分段轮询：0-2 分钟 3s，2-10 分钟 10s，10 分钟后 30s
    if (gen.status === 'pending' || gen.status === 'generating') {
      schedulePolling(id)
    } else {
      delete pollTimers[id]
    }
  } catch {
    delete pollTimers[id]
  }
}

async function loadActiveGenerations() {
  try {
    const res = await soraAPI.listGenerations({ status: 'pending,generating,completed,failed,cancelled', page_size: 50 })
    activeGenerations.value = res.data
    // 对进行中的任务启动轮询
    for (const gen of res.data) {
      if ((gen.status === 'pending' || gen.status === 'generating') && !pollTimers[gen.id]) {
        schedulePolling(gen.id)
      }
    }
  } catch (e) {
    console.error('Failed to load generations:', e)
  }
}

async function handleGenerate(req: GenerateRequest) {
  generating.value = true
  try {
    const res = await soraAPI.generate(req)
    // 添加到列表并开始轮询
    const gen = await soraAPI.getGeneration(res.generation_id)
    activeGenerations.value.unshift(gen)
    schedulePolling(gen.id)
  } catch (e: any) {
    console.error('Generate failed:', e)
    alert(e?.response?.data?.message || e?.message || 'Generation failed')
  } finally {
    generating.value = false
  }
}

async function handleCancel(id: number) {
  try {
    await soraAPI.cancelGeneration(id)
    const idx = activeGenerations.value.findIndex(g => g.id === id)
    if (idx >= 0) activeGenerations.value[idx].status = 'cancelled'
  } catch (e) {
    console.error('Cancel failed:', e)
  }
}

async function handleDelete(id: number) {
  try {
    await soraAPI.deleteGeneration(id)
    activeGenerations.value = activeGenerations.value.filter(g => g.id !== id)
  } catch (e) {
    console.error('Delete failed:', e)
  }
}

async function handleSave(id: number) {
  try {
    await soraAPI.saveToStorage(id)
    const gen = await soraAPI.getGeneration(id)
    const idx = activeGenerations.value.findIndex(g => g.id === id)
    if (idx >= 0) activeGenerations.value[idx] = gen
  } catch (e) {
    console.error('Save failed:', e)
  }
}

function handleRetry(gen: SoraGeneration) {
  handleGenerate({ model: gen.model, prompt: gen.prompt, media_type: gen.media_type })
}

onMounted(() => {
  loadActiveGenerations()
  requestNotificationPermission()
  window.addEventListener('beforeunload', beforeUnloadHandler)
})

onUnmounted(() => {
  Object.values(pollTimers).forEach(clearTimeout)
  pollTimers = {}
  stopTitleBlink()
  window.removeEventListener('beforeunload', beforeUnloadHandler)
})
</script>
