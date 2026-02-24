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

      <div v-if="!health.enabled" class="card border border-amber-300 bg-amber-50 p-5 text-amber-800 dark:border-amber-700 dark:bg-amber-900/20 dark:text-amber-200">
        {{ t('admin.dataManagement.actions.disabledHint') }}
      </div>

      <div class="card p-6">
        <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
          <div>
            <h3 class="text-base font-semibold text-gray-900 dark:text-white">
              {{ t('admin.dataManagement.sections.config.title') }}
            </h3>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {{ t('admin.dataManagement.sections.config.description') }}
            </p>
          </div>
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            :disabled="loadingConfig || !health.enabled"
            @click="loadConfig"
          >
            {{ loadingConfig ? t('common.loading') : t('admin.dataManagement.actions.reloadConfig') }}
          </button>
        </div>

        <div class="grid grid-cols-1 gap-4 md:grid-cols-2">
          <label class="block text-sm">
            <span class="mb-1 block text-gray-700 dark:text-gray-300">{{ t('admin.dataManagement.form.sourceMode') }}</span>
            <select v-model="config.source_mode" class="input w-full" :disabled="!health.enabled || loadingConfig">
              <option value="direct">direct</option>
              <option value="docker_exec">docker_exec</option>
            </select>
          </label>
          <label class="block text-sm">
            <span class="mb-1 block text-gray-700 dark:text-gray-300">{{ t('admin.dataManagement.form.backupRoot') }}</span>
            <input v-model="config.backup_root" class="input w-full" :disabled="!health.enabled || loadingConfig" />
          </label>
          <label class="block text-sm">
            <span class="mb-1 block text-gray-700 dark:text-gray-300">{{ t('admin.dataManagement.form.retentionDays') }}</span>
            <input
              v-model.number="config.retention_days"
              type="number"
              min="1"
              class="input w-full"
              :disabled="!health.enabled || loadingConfig"
            />
          </label>
          <label class="block text-sm">
            <span class="mb-1 block text-gray-700 dark:text-gray-300">{{ t('admin.dataManagement.form.keepLast') }}</span>
            <input
              v-model.number="config.keep_last"
              type="number"
              min="1"
              class="input w-full"
              :disabled="!health.enabled || loadingConfig"
            />
          </label>
        </div>

        <div class="mt-6 grid grid-cols-1 gap-6 xl:grid-cols-2">
          <div>
            <h4 class="text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('admin.dataManagement.form.postgres.title') }}
            </h4>
            <div class="mt-3 grid grid-cols-1 gap-3 md:grid-cols-2">
              <input v-model="config.postgres.host" class="input w-full" :placeholder="t('admin.dataManagement.form.postgres.host')" :disabled="!health.enabled" />
              <input v-model.number="config.postgres.port" type="number" class="input w-full" :placeholder="t('admin.dataManagement.form.postgres.port')" :disabled="!health.enabled" />
              <input v-model="config.postgres.user" class="input w-full" :placeholder="t('admin.dataManagement.form.postgres.user')" :disabled="!health.enabled" />
              <input
                v-model="config.postgres.password"
                type="password"
                class="input w-full"
                :placeholder="config.postgres.password_configured ? t('admin.dataManagement.form.secretConfigured') : t('admin.dataManagement.form.postgres.password')"
                :disabled="!health.enabled"
              />
              <input v-model="config.postgres.database" class="input w-full" :placeholder="t('admin.dataManagement.form.postgres.database')" :disabled="!health.enabled" />
              <input v-model="config.postgres.ssl_mode" class="input w-full" :placeholder="t('admin.dataManagement.form.postgres.sslMode')" :disabled="!health.enabled" />
              <input v-model="config.postgres.container_name" class="input w-full md:col-span-2" :placeholder="t('admin.dataManagement.form.postgres.containerName')" :disabled="!health.enabled" />
            </div>
          </div>

          <div>
            <h4 class="text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('admin.dataManagement.form.redis.title') }}
            </h4>
            <div class="mt-3 grid grid-cols-1 gap-3 md:grid-cols-2">
              <input v-model="config.redis.addr" class="input w-full md:col-span-2" :placeholder="t('admin.dataManagement.form.redis.addr')" :disabled="!health.enabled" />
              <input v-model="config.redis.username" class="input w-full" :placeholder="t('admin.dataManagement.form.redis.username')" :disabled="!health.enabled" />
              <input
                v-model="config.redis.password"
                type="password"
                class="input w-full"
                :placeholder="config.redis.password_configured ? t('admin.dataManagement.form.secretConfigured') : t('admin.dataManagement.form.redis.password')"
                :disabled="!health.enabled"
              />
              <input v-model.number="config.redis.db" type="number" class="input w-full" :placeholder="t('admin.dataManagement.form.redis.db')" :disabled="!health.enabled" />
              <input v-model="config.redis.container_name" class="input w-full md:col-span-2" :placeholder="t('admin.dataManagement.form.redis.containerName')" :disabled="!health.enabled" />
            </div>
          </div>
        </div>

        <div class="mt-6 rounded-lg border border-gray-200 p-4 dark:border-dark-700">
          <h4 class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ t('admin.dataManagement.sections.s3.title') }}
          </h4>
          <div class="mt-3 grid grid-cols-1 gap-3 md:grid-cols-2">
            <label class="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300 md:col-span-2">
              <input v-model="config.s3.enabled" type="checkbox" :disabled="!health.enabled" />
              <span>{{ t('admin.dataManagement.form.s3.enabled') }}</span>
            </label>
            <input v-model="config.s3.endpoint" class="input w-full" :placeholder="t('admin.dataManagement.form.s3.endpoint')" :disabled="!health.enabled" />
            <input v-model="config.s3.region" class="input w-full" :placeholder="t('admin.dataManagement.form.s3.region')" :disabled="!health.enabled" />
            <input v-model="config.s3.bucket" class="input w-full" :placeholder="t('admin.dataManagement.form.s3.bucket')" :disabled="!health.enabled" />
            <input v-model="config.s3.prefix" class="input w-full" :placeholder="t('admin.dataManagement.form.s3.prefix')" :disabled="!health.enabled" />
            <input v-model="config.s3.access_key_id" class="input w-full" :placeholder="t('admin.dataManagement.form.s3.accessKeyID')" :disabled="!health.enabled" />
            <input
              v-model="config.s3.secret_access_key"
              type="password"
              class="input w-full"
              :placeholder="config.s3.secret_access_key_configured ? t('admin.dataManagement.form.secretConfigured') : t('admin.dataManagement.form.s3.secretAccessKey')"
              :disabled="!health.enabled"
            />
            <label class="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
              <input v-model="config.s3.force_path_style" type="checkbox" :disabled="!health.enabled" />
              <span>{{ t('admin.dataManagement.form.s3.forcePathStyle') }}</span>
            </label>
            <label class="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
              <input v-model="config.s3.use_ssl" type="checkbox" :disabled="!health.enabled" />
              <span>{{ t('admin.dataManagement.form.s3.useSSL') }}</span>
            </label>
          </div>
          <div class="mt-4 flex flex-wrap gap-3">
            <button type="button" class="btn btn-secondary btn-sm" :disabled="!health.enabled || testingS3" @click="testS3Config">
              {{ testingS3 ? t('common.loading') : t('admin.dataManagement.actions.testS3') }}
            </button>
            <button type="button" class="btn btn-primary btn-sm" :disabled="!health.enabled || savingConfig" @click="saveConfig">
              {{ savingConfig ? t('common.loading') : t('admin.dataManagement.actions.saveConfig') }}
            </button>
          </div>
        </div>
      </div>

      <div class="card p-6">
        <h3 class="text-base font-semibold text-gray-900 dark:text-white">
          {{ t('admin.dataManagement.sections.backup.title') }}
        </h3>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.dataManagement.sections.backup.description') }}
        </p>

        <div class="mt-4 grid grid-cols-1 gap-3 md:grid-cols-4">
          <select v-model="createForm.backup_type" class="input w-full" :disabled="!health.enabled">
            <option value="postgres">postgres</option>
            <option value="redis">redis</option>
            <option value="full">full</option>
          </select>
          <label class="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
            <input v-model="createForm.upload_to_s3" type="checkbox" :disabled="!health.enabled" />
            <span>{{ t('admin.dataManagement.form.uploadToS3') }}</span>
          </label>
          <input
            v-model="createForm.idempotency_key"
            class="input w-full md:col-span-2"
            :placeholder="t('admin.dataManagement.form.idempotencyKey')"
            :disabled="!health.enabled"
          />
        </div>

        <div class="mt-4 flex flex-wrap gap-3">
          <button type="button" class="btn btn-primary btn-sm" :disabled="!health.enabled || creatingBackup" @click="createBackup">
            {{ creatingBackup ? t('common.loading') : t('admin.dataManagement.actions.createBackup') }}
          </button>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="loadingJobs" @click="refreshJobs">
            {{ loadingJobs ? t('common.loading') : t('admin.dataManagement.actions.refreshJobs') }}
          </button>
        </div>
      </div>

      <div class="card p-6">
        <div class="mb-4 flex items-center justify-between gap-3">
          <div>
            <h3 class="text-base font-semibold text-gray-900 dark:text-white">
              {{ t('admin.dataManagement.sections.history.title') }}
            </h3>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {{ t('admin.dataManagement.sections.history.description') }}
            </p>
          </div>
          <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.dataManagement.history.total', { count: jobs.length }) }}</span>
        </div>

        <div class="overflow-x-auto">
          <table class="w-full min-w-[920px] text-sm">
            <thead>
              <tr class="border-b border-gray-200 text-left text-xs uppercase tracking-wide text-gray-500 dark:border-dark-700 dark:text-gray-400">
                <th class="py-2 pr-4">{{ t('admin.dataManagement.history.columns.jobID') }}</th>
                <th class="py-2 pr-4">{{ t('admin.dataManagement.history.columns.type') }}</th>
                <th class="py-2 pr-4">{{ t('admin.dataManagement.history.columns.status') }}</th>
                <th class="py-2 pr-4">{{ t('admin.dataManagement.history.columns.triggeredBy') }}</th>
                <th class="py-2 pr-4">{{ t('admin.dataManagement.history.columns.finishedAt') }}</th>
                <th class="py-2 pr-4">{{ t('admin.dataManagement.history.columns.artifact') }}</th>
                <th class="py-2">{{ t('admin.dataManagement.history.columns.error') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="job in jobs" :key="job.job_id" class="border-b border-gray-100 align-top dark:border-dark-800">
                <td class="py-3 pr-4 font-mono text-xs">{{ job.job_id }}</td>
                <td class="py-3 pr-4">{{ job.backup_type }}</td>
                <td class="py-3 pr-4">
                  <span class="rounded px-2 py-0.5 text-xs" :class="statusClass(job.status)">
                    {{ statusText(job.status) }}
                  </span>
                </td>
                <td class="py-3 pr-4 text-xs">{{ job.triggered_by }}</td>
                <td class="py-3 pr-4 text-xs">{{ formatDate(job.finished_at || job.started_at) }}</td>
                <td class="py-3 pr-4 text-xs">
                  <div>{{ job.artifact?.local_path || '-' }}</div>
                  <div v-if="job.s3?.key" class="mt-1 text-gray-500 dark:text-gray-400">
                    s3://{{ job.s3.bucket }}/{{ job.s3.key }}
                  </div>
                </td>
                <td class="py-3 text-xs text-red-600 dark:text-red-400">{{ job.error_message || '-' }}</td>
              </tr>
              <tr v-if="jobs.length === 0">
                <td colspan="7" class="py-6 text-center text-sm text-gray-500 dark:text-gray-400">
                  {{ t('admin.dataManagement.history.empty') }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div v-if="nextPageToken" class="mt-4">
          <button type="button" class="btn btn-secondary btn-sm" :disabled="loadingJobs" @click="loadJobs(false)">
            {{ loadingJobs ? t('common.loading') : t('admin.dataManagement.actions.loadMore') }}
          </button>
        </div>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import {
  dataManagementAPI,
  type BackupAgentHealth,
  type BackupJob,
  type BackupJobStatus,
  type DataManagementConfig
} from '@/api/admin/dataManagement'
import { useAppStore } from '@/stores'

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const loadingConfig = ref(false)
const savingConfig = ref(false)
const testingS3 = ref(false)
const creatingBackup = ref(false)
const loadingJobs = ref(false)

const health = ref<BackupAgentHealth>({
  enabled: false,
  reason: 'BACKUP_AGENT_SOCKET_MISSING',
  socket_path: '/tmp/sub2api-backup.sock'
})

const config = ref<DataManagementConfig>(newDefaultConfig())
const jobs = ref<BackupJob[]>([])
const nextPageToken = ref('')

const createForm = ref({
  backup_type: 'full' as 'postgres' | 'redis' | 'full',
  upload_to_s3: true,
  idempotency_key: ''
})

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

async function loadConfig() {
  if (!health.value.enabled) {
    return
  }
  loadingConfig.value = true
  try {
    const result = await dataManagementAPI.getConfig()
    config.value = {
      ...newDefaultConfig(),
      ...result,
      postgres: {
        ...newDefaultConfig().postgres,
        ...result.postgres
      },
      redis: {
        ...newDefaultConfig().redis,
        ...result.redis
      },
      s3: {
        ...newDefaultConfig().s3,
        ...result.s3
      }
    }
  } catch (error) {
    appStore.showError((error as { message?: string })?.message || t('errors.networkError'))
  } finally {
    loadingConfig.value = false
  }
}

async function saveConfig() {
  if (!health.value.enabled) {
    return
  }
  savingConfig.value = true
  try {
    const updated = await dataManagementAPI.updateConfig(config.value)
    config.value = { ...config.value, ...updated }
    appStore.showSuccess(t('admin.dataManagement.actions.configSaved'))
  } catch (error) {
    appStore.showError((error as { message?: string })?.message || t('errors.networkError'))
  } finally {
    savingConfig.value = false
  }
}

async function testS3Config() {
  if (!health.value.enabled) {
    return
  }
  testingS3.value = true
  try {
    const result = await dataManagementAPI.testS3({
      endpoint: config.value.s3.endpoint,
      region: config.value.s3.region,
      bucket: config.value.s3.bucket,
      access_key_id: config.value.s3.access_key_id,
      secret_access_key: config.value.s3.secret_access_key || '',
      prefix: config.value.s3.prefix,
      force_path_style: config.value.s3.force_path_style,
      use_ssl: config.value.s3.use_ssl
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

async function createBackup() {
  if (!health.value.enabled) {
    return
  }
  creatingBackup.value = true
  try {
    const result = await dataManagementAPI.createBackupJob({
      backup_type: createForm.value.backup_type,
      upload_to_s3: createForm.value.upload_to_s3,
      idempotency_key: createForm.value.idempotency_key || undefined
    })
    appStore.showSuccess(
      t('admin.dataManagement.actions.jobCreated', {
        jobID: result.job_id,
        status: result.status
      })
    )
    if (!createForm.value.idempotency_key) {
      createForm.value.idempotency_key = ''
    }
    await refreshJobs()
  } catch (error) {
    appStore.showError((error as { message?: string })?.message || t('errors.networkError'))
  } finally {
    creatingBackup.value = false
  }
}

async function loadJobs(reset: boolean) {
  loadingJobs.value = true
  try {
    const result = await dataManagementAPI.listBackupJobs({
      page_size: 20,
      page_token: reset ? undefined : nextPageToken.value || undefined
    })
    if (reset) {
      jobs.value = result.items || []
    } else {
      jobs.value = [...jobs.value, ...(result.items || [])]
    }
    nextPageToken.value = result.next_page_token || ''
  } catch (error) {
    appStore.showError((error as { message?: string })?.message || t('errors.networkError'))
  } finally {
    loadingJobs.value = false
  }
}

async function refreshJobs() {
  nextPageToken.value = ''
  await loadJobs(true)
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

function statusClass(status: BackupJobStatus): string {
  switch (status) {
    case 'succeeded':
      return 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
    case 'failed':
      return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
    case 'partial_succeeded':
      return 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-300'
    case 'running':
      return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
    default:
      return 'bg-gray-100 text-gray-700 dark:bg-dark-800 dark:text-gray-300'
  }
}

function statusText(status: BackupJobStatus): string {
  const key = `admin.dataManagement.history.status.${status}`
  return t(key)
}

function newDefaultConfig(): DataManagementConfig {
  return {
    source_mode: 'direct',
    backup_root: '/var/lib/sub2api/backups',
    retention_days: 7,
    keep_last: 30,
    postgres: {
      host: '127.0.0.1',
      port: 5432,
      user: 'postgres',
      password: '',
      password_configured: false,
      database: 'sub2api',
      ssl_mode: 'disable',
      container_name: ''
    },
    redis: {
      addr: '127.0.0.1:6379',
      username: '',
      password: '',
      password_configured: false,
      db: 0,
      container_name: ''
    },
    s3: {
      enabled: false,
      endpoint: '',
      region: '',
      bucket: '',
      access_key_id: '',
      secret_access_key: '',
      secret_access_key_configured: false,
      prefix: '',
      force_path_style: false,
      use_ssl: true
    }
  }
}

onMounted(async () => {
  await loadAgentHealth()
  if (health.value.enabled) {
    await Promise.all([loadConfig(), refreshJobs()])
  }
})
</script>
