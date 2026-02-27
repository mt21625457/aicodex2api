<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="card p-6">
        <div class="flex flex-wrap items-start justify-between gap-4">
          <div>
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
              {{ t('admin.dataManagement.agent.title') }}
            </h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {{ t('admin.dataManagement.agent.description') }}
            </p>
          </div>

          <button
            type="button"
            class="btn btn-secondary btn-sm"
            :disabled="loading"
            @click="loadAgentHealth"
          >
            {{ loading ? t('common.loading') : t('admin.dataManagement.actions.refresh') }}
          </button>
        </div>

        <div class="mt-4 rounded-lg border border-gray-200 bg-gray-50 p-4 dark:border-dark-700 dark:bg-dark-900">
          <div class="flex flex-wrap items-center gap-2">
            <span
              class="inline-flex rounded-full px-2.5 py-0.5 text-xs font-medium"
              :class="statusBadgeClass"
            >
              {{ health.enabled ? t('common.enabled') : t('common.disabled') }}
            </span>
            <span class="text-sm text-gray-700 dark:text-gray-300">{{ statusMessage }}</span>
          </div>

          <div class="mt-3 text-xs text-gray-500 dark:text-gray-400">
            <div>
              {{ t('admin.dataManagement.agent.socketPath') }}:
              <code class="ml-1 rounded bg-gray-100 px-1.5 py-0.5 dark:bg-dark-800">{{
                health.socket_path
              }}</code>
            </div>
            <div v-if="!health.enabled" class="mt-2">
              {{ t('admin.dataManagement.agent.reasonLabel') }}:
              {{ reasonMessage }}
            </div>
            <div v-if="health.agent" class="mt-2 grid grid-cols-1 gap-1 sm:grid-cols-3">
              <span>{{ t('admin.dataManagement.agent.version') }}: {{ health.agent.version }}</span>
              <span>{{ t('admin.dataManagement.agent.status') }}: {{ health.agent.status }}</span>
              <span>{{ t('admin.dataManagement.agent.uptime') }}: {{ health.agent.uptime_seconds }}s</span>
            </div>
          </div>
        </div>
      </div>

      <div class="card p-6">
        <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
          <div>
            <h3 class="text-base font-semibold text-gray-900 dark:text-white">
              {{ t('admin.dataManagement.sections.s3.title') }}
            </h3>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {{ t('admin.dataManagement.sections.s3.description') }}
            </p>
          </div>
          <div class="flex flex-wrap gap-2">
            <button type="button" class="btn btn-secondary btn-sm" :disabled="!health.enabled" @click="startCreateProfile">
              {{ t('admin.dataManagement.actions.newProfile') }}
            </button>
            <button
              type="button"
              class="btn btn-secondary btn-sm"
              :disabled="!health.enabled || loadingProfiles"
              @click="loadS3Profiles"
            >
              {{ loadingProfiles ? t('common.loading') : t('admin.dataManagement.actions.reloadProfiles') }}
            </button>
          </div>
        </div>

        <div class="overflow-x-auto">
          <table class="w-full min-w-[880px] text-sm">
            <thead>
              <tr class="border-b border-gray-200 text-left text-xs uppercase tracking-wide text-gray-500 dark:border-dark-700 dark:text-gray-400">
                <th class="py-2 pr-4">{{ t('admin.dataManagement.s3Profiles.columns.profile') }}</th>
                <th class="py-2 pr-4">{{ t('admin.dataManagement.s3Profiles.columns.active') }}</th>
                <th class="py-2 pr-4">{{ t('admin.dataManagement.s3Profiles.columns.storage') }}</th>
                <th class="py-2 pr-4">{{ t('admin.dataManagement.s3Profiles.columns.updatedAt') }}</th>
                <th class="py-2">{{ t('admin.dataManagement.s3Profiles.columns.actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="profile in s3Profiles" :key="profile.profile_id" class="border-b border-gray-100 align-top dark:border-dark-800">
                <td class="py-3 pr-4">
                  <div class="font-mono text-xs">{{ profile.profile_id }}</div>
                  <div class="mt-1 text-xs text-gray-600 dark:text-gray-400">{{ profile.name }}</div>
                </td>
                <td class="py-3 pr-4">
                  <span
                    class="rounded px-2 py-0.5 text-xs"
                    :class="profile.is_active ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300' : 'bg-gray-100 text-gray-700 dark:bg-dark-800 dark:text-gray-300'"
                  >
                    {{ profile.is_active ? t('common.enabled') : t('common.disabled') }}
                  </span>
                </td>
                <td class="py-3 pr-4 text-xs">
                  <div>{{ profile.s3.bucket || '-' }}</div>
                  <div class="mt-1 text-gray-500 dark:text-gray-400">{{ profile.s3.region || '-' }}</div>
                </td>
                <td class="py-3 pr-4 text-xs">{{ formatDate(profile.updated_at) }}</td>
                <td class="py-3 text-xs">
                  <div class="flex flex-wrap gap-2">
                    <button type="button" class="btn btn-secondary btn-xs" :disabled="!health.enabled" @click="editS3Profile(profile.profile_id)">
                      {{ t('common.edit') }}
                    </button>
                    <button
                      v-if="!profile.is_active"
                      type="button"
                      class="btn btn-secondary btn-xs"
                      :disabled="!health.enabled || activatingProfile"
                      @click="activateS3Profile(profile.profile_id)"
                    >
                      {{ t('admin.dataManagement.actions.activateProfile') }}
                    </button>
                    <button
                      v-if="!profile.is_active"
                      type="button"
                      class="btn btn-danger btn-xs"
                      :disabled="!health.enabled || deletingProfile"
                      @click="removeS3Profile(profile.profile_id)"
                    >
                      {{ t('common.delete') }}
                    </button>
                  </div>
                </td>
              </tr>
              <tr v-if="s3Profiles.length === 0">
                <td colspan="5" class="py-6 text-center text-sm text-gray-500 dark:text-gray-400">
                  {{ t('admin.dataManagement.s3Profiles.empty') }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="mt-4 text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.dataManagement.s3Profiles.editHint') }}
        </div>
      </div>

      <!-- Sora S3 Storage Settings -->
      <div class="card p-6">
        <div class="mb-4">
          <h3 class="text-base font-semibold text-gray-900 dark:text-white">
            {{ t('admin.settings.soraS3.title') }}
          </h3>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.settings.soraS3.description') }}
          </p>
        </div>

        <div v-if="soraS3Loading" class="flex items-center gap-2 text-gray-500">
          <div class="h-4 w-4 animate-spin rounded-full border-b-2 border-primary-600"></div>
          {{ t('common.loading') }}
        </div>
        <template v-else>
          <!-- 启用开关 -->
          <div class="flex items-center justify-between">
            <div>
              <label class="font-medium text-gray-900 dark:text-white">{{ t('admin.settings.soraS3.enabled') }}</label>
              <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.settings.soraS3.enabledHint') }}</p>
            </div>
            <label class="relative inline-flex cursor-pointer items-center">
              <input v-model="soraS3Form.enabled" type="checkbox" class="peer sr-only" />
              <div class="peer h-6 w-11 rounded-full bg-gray-200 after:absolute after:left-[2px] after:top-[2px] after:h-5 after:w-5 after:rounded-full after:border after:border-gray-300 after:bg-white after:transition-all after:content-[''] peer-checked:bg-primary-600 peer-checked:after:translate-x-full peer-checked:after:border-white dark:bg-dark-700 dark:peer-checked:bg-primary-500"></div>
            </label>
          </div>
          <template v-if="soraS3Form.enabled">
            <div class="mt-4 grid grid-cols-1 gap-4 md:grid-cols-2">
              <div>
                <label class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.soraS3.endpoint') }}</label>
                <input v-model="soraS3Form.endpoint" type="text" class="input" placeholder="https://s3.amazonaws.com" />
              </div>
              <div>
                <label class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.soraS3.region') }}</label>
                <input v-model="soraS3Form.region" type="text" class="input" placeholder="us-east-1" />
              </div>
              <div>
                <label class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.soraS3.bucket') }}</label>
                <input v-model="soraS3Form.bucket" type="text" class="input" placeholder="my-sora-bucket" />
              </div>
              <div>
                <label class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.soraS3.prefix') }}</label>
                <input v-model="soraS3Form.prefix" type="text" class="input" placeholder="sora/" />
              </div>
              <div>
                <label class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.soraS3.accessKeyId') }}</label>
                <input v-model="soraS3Form.access_key_id" type="text" class="input" />
              </div>
              <div>
                <label class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.soraS3.secretAccessKey') }}</label>
                <input v-model="soraS3Form.secret_access_key" type="password" class="input" :placeholder="soraS3Form.secret_access_key_configured ? t('admin.settings.soraS3.secretConfigured') : ''" />
              </div>
              <div>
                <label class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.soraS3.cdnUrl') }}</label>
                <input v-model="soraS3Form.cdn_url" type="text" class="input" placeholder="https://cdn.example.com" />
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.settings.soraS3.cdnUrlHint') }}</p>
              </div>
              <div>
                <label class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.soraS3.defaultQuota') }}</label>
                <div class="flex items-center gap-2">
                  <input v-model.number="soraS3Form.default_storage_quota_gb" type="number" min="0" step="0.1" class="input" placeholder="10" />
                  <span class="shrink-0 text-sm text-gray-500">GB</span>
                </div>
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.settings.soraS3.defaultQuotaHint') }}</p>
              </div>
            </div>
            <div class="mt-4 flex items-center gap-2">
              <label class="relative inline-flex cursor-pointer items-center">
                <input v-model="soraS3Form.force_path_style" type="checkbox" class="peer sr-only" />
                <div class="peer h-6 w-11 rounded-full bg-gray-200 after:absolute after:left-[2px] after:top-[2px] after:h-5 after:w-5 after:rounded-full after:border after:border-gray-300 after:bg-white after:transition-all after:content-[''] peer-checked:bg-primary-600 peer-checked:after:translate-x-full peer-checked:after:border-white dark:bg-dark-700 dark:peer-checked:bg-primary-500"></div>
              </label>
              <span class="text-sm text-gray-700 dark:text-gray-300">{{ t('admin.settings.soraS3.forcePathStyle') }}</span>
            </div>
          </template>
          <!-- 操作按钮 -->
          <div class="mt-4 flex justify-end gap-3 border-t border-gray-100 pt-4 dark:border-dark-700">
            <button v-if="soraS3Form.enabled" type="button" @click="testSoraS3" :disabled="soraS3Testing" class="btn btn-secondary btn-sm">
              {{ soraS3Testing ? t('admin.settings.soraS3.testing') : t('admin.settings.soraS3.testConnection') }}
            </button>
            <button type="button" @click="saveSoraS3Settings" :disabled="soraS3Saving" class="btn btn-primary btn-sm">
              {{ soraS3Saving ? t('admin.settings.saving') : t('admin.settings.saveSettings') }}
            </button>
          </div>
        </template>
      </div>

    </div>

    <Teleport to="body">
      <Transition name="dm-drawer-mask">
        <div
          v-if="profileDrawerOpen"
          class="fixed inset-0 z-[52] bg-black/40 backdrop-blur-sm"
          @click="closeProfileDrawer"
        ></div>
      </Transition>

      <Transition name="dm-drawer-panel">
        <div
          v-if="profileDrawerOpen"
          class="fixed inset-y-0 right-0 z-[53] flex h-full w-full max-w-2xl flex-col border-l border-gray-200 bg-white shadow-2xl dark:border-dark-700 dark:bg-dark-900"
        >
          <div class="flex items-center justify-between border-b border-gray-200 px-4 py-3 dark:border-dark-700">
            <h4 class="text-sm font-semibold text-gray-900 dark:text-white">
              {{ creatingProfile ? t('admin.dataManagement.s3Profiles.createTitle') : t('admin.dataManagement.s3Profiles.editTitle') }}
            </h4>
            <button
              type="button"
              class="rounded p-1 text-gray-500 hover:bg-gray-100 hover:text-gray-700 dark:text-gray-400 dark:hover:bg-dark-800 dark:hover:text-gray-200"
              @click="closeProfileDrawer"
            >
              ✕
            </button>
          </div>

          <div class="flex-1 overflow-y-auto p-4">
            <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
              <input
                v-model="profileForm.profile_id"
                class="input w-full"
                :placeholder="t('admin.dataManagement.form.s3.profileID')"
                :disabled="!health.enabled || !creatingProfile"
              />
              <input
                v-model="profileForm.name"
                class="input w-full"
                :placeholder="t('admin.dataManagement.form.s3.profileName')"
                :disabled="!health.enabled"
              />
              <label class="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300 md:col-span-2">
                <input v-model="profileForm.enabled" type="checkbox" :disabled="!health.enabled" />
                <span>{{ t('admin.dataManagement.form.s3.enabled') }}</span>
              </label>
              <input v-model="profileForm.endpoint" class="input w-full" :placeholder="t('admin.dataManagement.form.s3.endpoint')" :disabled="!health.enabled" />
              <input v-model="profileForm.region" class="input w-full" :placeholder="t('admin.dataManagement.form.s3.region')" :disabled="!health.enabled" />
              <input v-model="profileForm.bucket" class="input w-full" :placeholder="t('admin.dataManagement.form.s3.bucket')" :disabled="!health.enabled" />
              <input v-model="profileForm.prefix" class="input w-full" :placeholder="t('admin.dataManagement.form.s3.prefix')" :disabled="!health.enabled" />
              <input v-model="profileForm.access_key_id" class="input w-full" :placeholder="t('admin.dataManagement.form.s3.accessKeyID')" :disabled="!health.enabled" />
              <input
                v-model="profileForm.secret_access_key"
                type="password"
                class="input w-full"
                :placeholder="profileForm.secret_access_key_configured ? t('admin.dataManagement.form.secretConfigured') : t('admin.dataManagement.form.s3.secretAccessKey')"
                :disabled="!health.enabled"
              />
              <label class="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                <input v-model="profileForm.force_path_style" type="checkbox" :disabled="!health.enabled" />
                <span>{{ t('admin.dataManagement.form.s3.forcePathStyle') }}</span>
              </label>
              <label class="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                <input v-model="profileForm.use_ssl" type="checkbox" :disabled="!health.enabled" />
                <span>{{ t('admin.dataManagement.form.s3.useSSL') }}</span>
              </label>
              <label v-if="creatingProfile" class="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300 md:col-span-2">
                <input v-model="profileForm.set_active" type="checkbox" :disabled="!health.enabled" />
                <span>{{ t('admin.dataManagement.form.s3.setActive') }}</span>
              </label>
            </div>
          </div>

          <div class="flex flex-wrap justify-end gap-2 border-t border-gray-200 p-4 dark:border-dark-700">
            <button type="button" class="btn btn-secondary btn-sm" :disabled="!health.enabled" @click="closeProfileDrawer">
              {{ t('common.cancel') }}
            </button>
            <button type="button" class="btn btn-secondary btn-sm" :disabled="!health.enabled || testingS3" @click="testProfileS3Config">
              {{ testingS3 ? t('common.loading') : t('admin.dataManagement.actions.testS3') }}
            </button>
            <button type="button" class="btn btn-primary btn-sm" :disabled="!health.enabled || savingProfile" @click="saveS3Profile">
              {{ savingProfile ? t('common.loading') : t('admin.dataManagement.actions.saveProfile') }}
            </button>
          </div>
        </div>
      </Transition>
    </Teleport>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import {
  dataManagementAPI,
  type BackupAgentHealth,
  type DataManagementS3Profile
} from '@/api/admin/dataManagement'
import { adminAPI } from '@/api'
import { useAppStore } from '@/stores'

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const soraS3Loading = ref(true)
const soraS3Saving = ref(false)
const soraS3Testing = ref(false)
const testingS3 = ref(false)
const loadingProfiles = ref(false)
const savingProfile = ref(false)
const activatingProfile = ref(false)
const deletingProfile = ref(false)
const creatingProfile = ref(false)
const profileDrawerOpen = ref(false)

const health = ref<BackupAgentHealth>({
  enabled: false,
  reason: 'DATA_MANAGEMENT_AGENT_SOCKET_MISSING',
  socket_path: '/tmp/sub2api-datamanagement.sock'
})

const s3Profiles = ref<DataManagementS3Profile[]>([])
const selectedProfileID = ref('')

type S3ProfileForm = {
  profile_id: string
  name: string
  enabled: boolean
  endpoint: string
  region: string
  bucket: string
  access_key_id: string
  secret_access_key: string
  secret_access_key_configured: boolean
  prefix: string
  force_path_style: boolean
  use_ssl: boolean
  set_active: boolean
}

const soraS3Form = ref({
  enabled: false,
  endpoint: '',
  region: '',
  bucket: '',
  access_key_id: '',
  secret_access_key: '',
  secret_access_key_configured: false,
  prefix: 'sora/',
  force_path_style: false,
  cdn_url: '',
  default_storage_quota_gb: 10,
  default_storage_quota_bytes: 0
})

const profileForm = ref<S3ProfileForm>(newDefaultS3ProfileForm())

const statusBadgeClass = computed(() => {
  return health.value.enabled
    ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
    : 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
})

const reasonMessage = computed(() => {
  if (health.value.enabled) {
    return ''
  }

  const reasonKeyMap: Record<string, string> = {
    DATA_MANAGEMENT_AGENT_SOCKET_MISSING: 'admin.dataManagement.agent.reason.DATA_MANAGEMENT_AGENT_SOCKET_MISSING',
    DATA_MANAGEMENT_AGENT_UNAVAILABLE: 'admin.dataManagement.agent.reason.DATA_MANAGEMENT_AGENT_UNAVAILABLE',
    // 向后兼容旧 reason code
    BACKUP_AGENT_SOCKET_MISSING: 'admin.dataManagement.agent.reason.BACKUP_AGENT_SOCKET_MISSING',
    BACKUP_AGENT_UNAVAILABLE: 'admin.dataManagement.agent.reason.BACKUP_AGENT_UNAVAILABLE'
  }

  const matched = reasonKeyMap[health.value.reason]
  if (!matched) {
    return t('admin.dataManagement.agent.reason.UNKNOWN')
  }
  return t(matched)
})

const statusMessage = computed(() => {
  return health.value.enabled
    ? t('admin.dataManagement.agent.enabled')
    : t('admin.dataManagement.agent.disabled')
})

async function loadAgentHealth() {
  loading.value = true
  try {
    health.value = await dataManagementAPI.getAgentHealth()
  } catch (error) {
    const message = (error as { message?: string })?.message || t('errors.networkError')
    appStore.showError(message)
  } finally {
    loading.value = false
  }
}

async function testProfileS3Config() {
  if (!health.value.enabled) {
    return
  }
  testingS3.value = true
  try {
    const result = await dataManagementAPI.testS3({
      endpoint: profileForm.value.endpoint,
      region: profileForm.value.region,
      bucket: profileForm.value.bucket,
      access_key_id: profileForm.value.access_key_id,
      secret_access_key: profileForm.value.secret_access_key || '',
      prefix: profileForm.value.prefix,
      force_path_style: profileForm.value.force_path_style,
      use_ssl: profileForm.value.use_ssl
    })
    if (result.ok) {
      appStore.showSuccess(result.message || t('admin.dataManagement.actions.s3TestOK'))
    } else {
      appStore.showError(result.message || t('admin.dataManagement.actions.s3TestFailed'))
    }
  } catch (error) {
    appStore.showError((error as { message?: string })?.message || t('errors.networkError'))
  } finally {
    testingS3.value = false
  }
}

async function loadS3Profiles() {
  if (!health.value.enabled) {
    return
  }
  loadingProfiles.value = true
  try {
    const result = await dataManagementAPI.listS3Profiles()
    s3Profiles.value = result.items || []

    if (!creatingProfile.value) {
      const stillExists = selectedProfileID.value
        ? s3Profiles.value.some((item) => item.profile_id === selectedProfileID.value)
        : false
      if (!stillExists) {
        selectedProfileID.value = pickPreferredProfileID()
      }
      syncProfileFormWithSelection()
    }

  } catch (error) {
    appStore.showError((error as { message?: string })?.message || t('errors.networkError'))
  } finally {
    loadingProfiles.value = false
  }
}

function startCreateProfile() {
  creatingProfile.value = true
  selectedProfileID.value = ''
  profileForm.value = newDefaultS3ProfileForm()
  profileDrawerOpen.value = true
}

function editS3Profile(profileID: string) {
  selectedProfileID.value = profileID
  creatingProfile.value = false
  syncProfileFormWithSelection()
  profileDrawerOpen.value = true
}

function closeProfileDrawer() {
  profileDrawerOpen.value = false
  if (creatingProfile.value) {
    creatingProfile.value = false
    selectedProfileID.value = pickPreferredProfileID()
    syncProfileFormWithSelection()
  }
}

async function saveS3Profile() {
  if (!health.value.enabled) {
    return
  }
  if (!profileForm.value.name.trim()) {
    appStore.showError(t('admin.dataManagement.actions.profileNameRequired'))
    return
  }
  if (creatingProfile.value && !profileForm.value.profile_id.trim()) {
    appStore.showError(t('admin.dataManagement.actions.profileIDRequired'))
    return
  }
  if (!creatingProfile.value && !selectedProfileID.value) {
    appStore.showError(t('admin.dataManagement.actions.profileSelectRequired'))
    return
  }

  savingProfile.value = true
  try {
    if (creatingProfile.value) {
      const created = await dataManagementAPI.createS3Profile({
        profile_id: profileForm.value.profile_id.trim(),
        name: profileForm.value.name.trim(),
        enabled: profileForm.value.enabled,
        endpoint: profileForm.value.endpoint,
        region: profileForm.value.region,
        bucket: profileForm.value.bucket,
        access_key_id: profileForm.value.access_key_id,
        secret_access_key: profileForm.value.secret_access_key || undefined,
        prefix: profileForm.value.prefix,
        force_path_style: profileForm.value.force_path_style,
        use_ssl: profileForm.value.use_ssl,
        set_active: profileForm.value.set_active
      })
      selectedProfileID.value = created.profile_id
      creatingProfile.value = false
      profileDrawerOpen.value = false
      appStore.showSuccess(t('admin.dataManagement.actions.profileCreated'))
    } else {
      await dataManagementAPI.updateS3Profile(selectedProfileID.value, {
        name: profileForm.value.name.trim(),
        enabled: profileForm.value.enabled,
        endpoint: profileForm.value.endpoint,
        region: profileForm.value.region,
        bucket: profileForm.value.bucket,
        access_key_id: profileForm.value.access_key_id,
        secret_access_key: profileForm.value.secret_access_key || undefined,
        prefix: profileForm.value.prefix,
        force_path_style: profileForm.value.force_path_style,
        use_ssl: profileForm.value.use_ssl
      })
      profileDrawerOpen.value = false
      appStore.showSuccess(t('admin.dataManagement.actions.profileSaved'))
    }

    await loadS3Profiles()
  } catch (error) {
    appStore.showError((error as { message?: string })?.message || t('errors.networkError'))
  } finally {
    savingProfile.value = false
  }
}

async function activateS3Profile(profileID: string) {
  if (!health.value.enabled) {
    return
  }
  activatingProfile.value = true
  try {
    await dataManagementAPI.setActiveS3Profile(profileID)
    appStore.showSuccess(t('admin.dataManagement.actions.profileActivated'))
    await loadS3Profiles()
  } catch (error) {
    appStore.showError((error as { message?: string })?.message || t('errors.networkError'))
  } finally {
    activatingProfile.value = false
  }
}

async function removeS3Profile(profileID: string) {
  if (!health.value.enabled) {
    return
  }
  if (!window.confirm(t('admin.dataManagement.s3Profiles.deleteConfirm', { profileID }))) {
    return
  }

  deletingProfile.value = true
  try {
    await dataManagementAPI.deleteS3Profile(profileID)
    if (selectedProfileID.value === profileID) {
      selectedProfileID.value = ''
    }
    appStore.showSuccess(t('admin.dataManagement.actions.profileDeleted'))
    await loadS3Profiles()
  } catch (error) {
    appStore.showError((error as { message?: string })?.message || t('errors.networkError'))
  } finally {
    deletingProfile.value = false
  }
}

function formatDate(value?: string): string {
  if (!value) {
    return '-'
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return value
  }
  return date.toLocaleString()
}

function pickPreferredProfileID(): string {
  const active = s3Profiles.value.find((item) => item.is_active)
  if (active) {
    return active.profile_id
  }
  return s3Profiles.value[0]?.profile_id || ''
}

function syncProfileFormWithSelection() {
  const profile = s3Profiles.value.find((item) => item.profile_id === selectedProfileID.value)
  profileForm.value = newDefaultS3ProfileForm(profile)
}

function newDefaultS3ProfileForm(profile?: DataManagementS3Profile): S3ProfileForm {
  if (!profile) {
    return {
      profile_id: '',
      name: '',
      enabled: false,
      endpoint: '',
      region: '',
      bucket: '',
      access_key_id: '',
      secret_access_key: '',
      secret_access_key_configured: false,
      prefix: '',
      force_path_style: false,
      use_ssl: true,
      set_active: false
    }
  }

  return {
    profile_id: profile.profile_id,
    name: profile.name,
    enabled: profile.s3?.enabled || false,
    endpoint: profile.s3?.endpoint || '',
    region: profile.s3?.region || '',
    bucket: profile.s3?.bucket || '',
    access_key_id: profile.s3?.access_key_id || '',
    secret_access_key: '',
    secret_access_key_configured:
      Boolean(profile.secret_access_key_configured) || Boolean(profile.s3?.secret_access_key_configured),
    prefix: profile.s3?.prefix || '',
    force_path_style: profile.s3?.force_path_style || false,
    use_ssl: profile.s3?.use_ssl ?? true,
    set_active: false
  }
}

// Sora S3 方法
async function loadSoraS3Settings() {
  soraS3Loading.value = true
  try {
    const settings = await adminAPI.settings.getSoraS3Settings()
    soraS3Form.value = {
      ...soraS3Form.value,
      ...settings,
      secret_access_key: '',
      default_storage_quota_gb: Number((settings.default_storage_quota_bytes / (1024 * 1024 * 1024)).toFixed(2))
    }
  } catch {
    // Sora S3 可能未配置，忽略错误
  } finally {
    soraS3Loading.value = false
  }
}

async function saveSoraS3Settings() {
  soraS3Saving.value = true
  try {
    const updated = await adminAPI.settings.updateSoraS3Settings({
      enabled: soraS3Form.value.enabled,
      endpoint: soraS3Form.value.endpoint,
      region: soraS3Form.value.region,
      bucket: soraS3Form.value.bucket,
      access_key_id: soraS3Form.value.access_key_id,
      secret_access_key: soraS3Form.value.secret_access_key || undefined,
      prefix: soraS3Form.value.prefix,
      force_path_style: soraS3Form.value.force_path_style,
      cdn_url: soraS3Form.value.cdn_url,
      default_storage_quota_bytes: Math.round((soraS3Form.value.default_storage_quota_gb || 0) * 1024 * 1024 * 1024)
    })
    soraS3Form.value = {
      ...soraS3Form.value,
      ...updated,
      secret_access_key: '',
      default_storage_quota_gb: Number((updated.default_storage_quota_bytes / (1024 * 1024 * 1024)).toFixed(2))
    }
    appStore.showSuccess(t('admin.settings.soraS3.saved'))
  } catch (error: any) {
    appStore.showError(t('admin.settings.soraS3.saveFailed') + ': ' + (error.message || t('errors.networkError')))
  } finally {
    soraS3Saving.value = false
  }
}

async function testSoraS3() {
  soraS3Testing.value = true
  try {
    const result = await adminAPI.settings.testSoraS3Connection({
      enabled: soraS3Form.value.enabled,
      endpoint: soraS3Form.value.endpoint,
      region: soraS3Form.value.region,
      bucket: soraS3Form.value.bucket,
      access_key_id: soraS3Form.value.access_key_id,
      secret_access_key: soraS3Form.value.secret_access_key || undefined,
      prefix: soraS3Form.value.prefix,
      force_path_style: soraS3Form.value.force_path_style,
      cdn_url: soraS3Form.value.cdn_url,
      default_storage_quota_bytes: Math.round((soraS3Form.value.default_storage_quota_gb || 0) * 1024 * 1024 * 1024)
    })
    appStore.showSuccess(result.message || t('admin.settings.soraS3.testSuccess'))
  } catch (error: any) {
    appStore.showError(t('admin.settings.soraS3.testFailed') + ': ' + (error.response?.data?.message || error.message || t('errors.networkError')))
  } finally {
    soraS3Testing.value = false
  }
}

onMounted(async () => {
  loadSoraS3Settings()
  await loadAgentHealth()
  if (health.value.enabled) {
    await loadS3Profiles()
  }
})
</script>

<style scoped>
.dm-drawer-mask-enter-active,
.dm-drawer-mask-leave-active {
  transition: opacity 0.2s ease;
}

.dm-drawer-mask-enter-from,
.dm-drawer-mask-leave-to {
  opacity: 0;
}

.dm-drawer-panel-enter-active,
.dm-drawer-panel-leave-active {
  transition:
    transform 0.24s cubic-bezier(0.22, 1, 0.36, 1),
    opacity 0.2s ease;
}

.dm-drawer-panel-enter-from,
.dm-drawer-panel-leave-to {
  opacity: 0.96;
  transform: translateX(100%);
}

@media (prefers-reduced-motion: reduce) {
  .dm-drawer-mask-enter-active,
  .dm-drawer-mask-leave-active,
  .dm-drawer-panel-enter-active,
  .dm-drawer-panel-leave-active {
    transition-duration: 0s;
  }
}
</style>
