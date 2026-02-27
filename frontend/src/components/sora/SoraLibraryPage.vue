<template>
  <div class="space-y-4">
    <!-- 筛选栏 -->
    <div class="flex items-center gap-2">
      <button
        v-for="f in filters"
        :key="f.value"
        class="rounded-full px-3 py-1 text-sm transition-colors"
        :class="activeFilter === f.value
          ? 'bg-primary-600 text-white'
          : 'bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-dark-700 dark:text-gray-400 dark:hover:bg-dark-600'"
        @click="activeFilter = f.value"
      >
        {{ f.label }}
      </button>
    </div>

    <!-- 作品网格 -->
    <div v-if="filteredItems.length > 0" class="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-4">
      <div
        v-for="item in filteredItems"
        :key="item.id"
        class="group relative overflow-hidden rounded-lg bg-gray-100 dark:bg-dark-700"
      >
        <!-- 媒体 -->
        <div class="aspect-video">
          <video
            v-if="item.media_type === 'video' && item.media_url"
            :src="item.media_url"
            class="h-full w-full object-cover"
            muted
            loop
            @mouseenter="($event.target as HTMLVideoElement).play()"
            @mouseleave="($event.target as HTMLVideoElement).pause()"
          />
          <img
            v-else-if="item.media_url"
            :src="item.media_url"
            class="h-full w-full object-cover"
            alt=""
          />
        </div>

        <!-- 悬浮操作 -->
        <div class="absolute inset-0 flex items-end bg-gradient-to-t from-black/60 via-transparent to-transparent opacity-0 transition-opacity group-hover:opacity-100">
          <div class="w-full p-3">
            <p class="mb-2 line-clamp-2 text-xs text-white">{{ item.prompt }}</p>
            <div class="flex items-center gap-2">
              <a
                v-if="item.media_url"
                :href="item.media_url"
                target="_blank"
                class="rounded bg-white/20 px-2 py-1 text-xs text-white backdrop-blur-sm transition-colors hover:bg-white/30"
              >
                {{ t('sora.download') }}
              </a>
              <button
                class="rounded bg-white/20 px-2 py-1 text-xs text-white backdrop-blur-sm transition-colors hover:bg-red-500/80"
                @click="handleDelete(item.id)"
              >
                {{ t('sora.delete') }}
              </button>
            </div>
          </div>
        </div>

        <!-- 类型角标 -->
        <span class="absolute right-2 top-2 rounded bg-black/50 px-1.5 py-0.5 text-[10px] text-white backdrop-blur-sm">
          {{ item.media_type === 'video' ? 'VIDEO' : 'IMAGE' }}
        </span>
      </div>
    </div>

    <!-- 空状态 -->
    <div v-else class="flex flex-col items-center justify-center py-16 text-center">
      <svg class="mb-4 h-12 w-12 text-gray-300 dark:text-gray-600" fill="none" viewBox="0 0 24 24" stroke="currentColor">
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M2.25 15.75l5.159-5.159a2.25 2.25 0 013.182 0l5.159 5.159m-1.5-1.5l1.409-1.409a2.25 2.25 0 013.182 0l2.909 2.909M3.75 21h16.5A2.25 2.25 0 0022.5 18.75V5.25A2.25 2.25 0 0020.25 3H3.75A2.25 2.25 0 001.5 5.25v13.5A2.25 2.25 0 003.75 21z" />
      </svg>
      <p class="text-gray-500 dark:text-gray-400">{{ t('sora.noSavedWorks') }}</p>
      <p class="mt-1 text-xs text-gray-400 dark:text-gray-500">{{ t('sora.saveWorksHint') }}</p>
    </div>

    <!-- 加载更多 -->
    <div v-if="hasMore" class="flex justify-center">
      <button
        class="rounded-lg px-4 py-2 text-sm text-gray-500 transition-colors hover:bg-gray-100 dark:text-gray-400 dark:hover:bg-dark-700"
        :disabled="loading"
        @click="loadMore"
      >
        {{ loading ? t('sora.loading') : t('sora.loadMore') }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import soraAPI, { type SoraGeneration } from '@/api/sora'

const { t } = useI18n()

const items = ref<SoraGeneration[]>([])
const loading = ref(false)
const page = ref(1)
const hasMore = ref(true)
const activeFilter = ref('all')

const filters = computed(() => [
  { value: 'all', label: t('sora.filterAll') },
  { value: 'video', label: t('sora.filterVideo') },
  { value: 'image', label: t('sora.filterImage') }
])

const filteredItems = computed(() => {
  if (activeFilter.value === 'all') return items.value
  return items.value.filter(i => i.media_type === activeFilter.value)
})

async function loadItems(pageNum: number) {
  loading.value = true
  try {
    const res = await soraAPI.listGenerations({
      status: 'completed',
      storage_type: 's3,local',
      page: pageNum,
      page_size: 20
    })
    if (pageNum === 1) {
      items.value = res.data
    } else {
      items.value.push(...res.data)
    }
    hasMore.value = items.value.length < res.total
  } catch (e) {
    console.error('Failed to load library:', e)
  } finally {
    loading.value = false
  }
}

function loadMore() {
  page.value++
  loadItems(page.value)
}

async function handleDelete(id: number) {
  if (!confirm(t('sora.confirmDelete'))) return
  try {
    await soraAPI.deleteGeneration(id)
    items.value = items.value.filter(i => i.id !== id)
  } catch (e) {
    console.error('Delete failed:', e)
  }
}

onMounted(() => loadItems(1))
</script>
