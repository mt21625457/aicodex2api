<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl space-y-4">
      <!-- 功能未启用提示 -->
      <div v-if="!soraEnabled" class="flex flex-col items-center justify-center py-24 text-center">
        <svg class="mb-4 h-16 w-16 text-gray-300 dark:text-gray-600" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M9.813 15.904L9 18.75l-.813-2.846a4.5 4.5 0 00-3.09-3.09L2.25 12l2.846-.813a4.5 4.5 0 003.09-3.09L9 5.25l.813 2.846a4.5 4.5 0 003.09 3.09L15.75 12l-2.846.813a4.5 4.5 0 00-3.09 3.09zM18.259 8.715L18 9.75l-.259-1.035a3.375 3.375 0 00-2.455-2.456L14.25 6l1.036-.259a3.375 3.375 0 002.455-2.456L18 2.25l.259 1.035a3.375 3.375 0 002.455 2.456L21.75 6l-1.036.259a3.375 3.375 0 00-2.455 2.456z" />
        </svg>
        <h2 class="text-xl font-semibold text-gray-700 dark:text-gray-300">{{ t('sora.notEnabled') }}</h2>
        <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">{{ t('sora.notEnabledDesc') }}</p>
      </div>

      <!-- Sora 主界面 -->
      <template v-else>
        <!-- Tab 导航 + 配额 -->
        <div class="flex items-center justify-between">
          <div class="flex space-x-1 rounded-lg bg-gray-100 p-1 dark:bg-dark-800">
            <button
              v-for="tab in tabs"
              :key="tab.key"
              class="rounded-md px-4 py-2 text-sm font-medium transition-colors"
              :class="activeTab === tab.key
                ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-700 dark:text-white'
                : 'text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200'"
              @click="activeTab = tab.key"
            >
              {{ tab.label }}
            </button>
          </div>
          <SoraQuotaBar v-if="quota" :quota="quota" />
        </div>

        <!-- 生成页 -->
        <SoraGeneratePage v-if="activeTab === 'generate'" />

        <!-- 作品库页 -->
        <SoraLibraryPage v-else-if="activeTab === 'library'" />
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores'
import AppLayout from '@/components/layout/AppLayout.vue'
import SoraQuotaBar from '@/components/sora/SoraQuotaBar.vue'
import SoraGeneratePage from '@/components/sora/SoraGeneratePage.vue'
import SoraLibraryPage from '@/components/sora/SoraLibraryPage.vue'
import soraAPI, { type QuotaInfo } from '@/api/sora'

const { t } = useI18n()
const appStore = useAppStore()

const soraEnabled = computed(() => appStore.cachedPublicSettings?.sora_client_enabled ?? false)

const activeTab = ref<'generate' | 'library'>('generate')
const quota = ref<QuotaInfo | null>(null)

const tabs = computed(() => [
  { key: 'generate' as const, label: t('sora.tabGenerate') },
  { key: 'library' as const, label: t('sora.tabLibrary') }
])

onMounted(async () => {
  if (!soraEnabled.value) return
  try {
    quota.value = await soraAPI.getQuota()
  } catch {
    // 配额查询失败不阻塞页面
  }
})
</script>
