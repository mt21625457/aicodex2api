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

        <div class="mt-6 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-gray-200 bg-gray-50 p-4 dark:border-dark-700 dark:bg-dark-900">
          <div class="text-sm text-gray-700 dark:text-gray-300">
            <div>
              {{ t('admin.dataManagement.form.activePostgresProfile') }}:
              <code class="ml-1 rounded bg-gray-100 px-1.5 py-0.5 text-xs dark:bg-dark-800">{{
                config.active_postgres_profile_id || '-'
              }}</code>
            </div>
            <div class="mt-2">
              {{ t('admin.dataManagement.form.activeRedisProfile') }}:
              <code class="ml-1 rounded bg-gray-100 px-1.5 py-0.5 text-xs dark:bg-dark-800">{{
                config.active_redis_profile_id || '-'
              }}</code>
            </div>
            <div class="mt-2">
              {{ t('admin.dataManagement.form.activeS3Profile') }}:
              <code class="ml-1 rounded bg-gray-100 px-1.5 py-0.5 text-xs dark:bg-dark-800">{{
                config.active_s3_profile_id || '-'
              }}</code>
            </div>
          </div>
          <button type="button" class="btn btn-primary btn-sm" :disabled="!health.enabled || savingConfig" @click="saveConfig">
            {{ savingConfig ? t('common.loading') : t('admin.dataManagement.actions.saveConfig') }}
          </button>
        </div>

        <div class="mt-6 grid grid-cols-1 gap-6 xl:grid-cols-2">
          <div>
            <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
              <h4 class="text-sm font-semibold text-gray-900 dark:text-white">
                {{ t('admin.dataManagement.form.postgres.title') }}
              </h4>
              <div class="flex gap-2">
                <button type="button" class="btn btn-secondary btn-xs" :disabled="!health.enabled" @click="startCreateSourceProfile('postgres')">
                  {{ t('admin.dataManagement.actions.newSourceProfile') }}
                </button>
                <button type="button" class="btn btn-secondary btn-xs" :disabled="!health.enabled || loadingSourceProfiles" @click="loadSourceProfiles">
                  {{ loadingSourceProfiles ? t('common.loading') : t('admin.dataManagement.actions.reloadSourceProfiles') }}
                </button>
              </div>
            </div>

            <div class="overflow-x-auto rounded-lg border border-gray-200 dark:border-dark-700">
              <table class="w-full min-w-[680px] text-sm">
                <thead>
                  <tr class="border-b border-gray-200 bg-gray-50 text-left text-xs uppercase tracking-wide text-gray-500 dark:border-dark-700 dark:bg-dark-900 dark:text-gray-400">
                    <th class="py-2 pl-3 pr-2">{{ t('admin.dataManagement.sourceProfiles.columns.profile') }}</th>
                    <th class="py-2 pr-2">{{ t('admin.dataManagement.sourceProfiles.columns.active') }}</th>
                    <th class="py-2 pr-2">{{ t('admin.dataManagement.sourceProfiles.columns.connection') }}</th>
                    <th class="py-2 pr-2">{{ t('admin.dataManagement.sourceProfiles.columns.database') }}</th>
                    <th class="py-2 pr-2">{{ t('admin.dataManagement.sourceProfiles.columns.updatedAt') }}</th>
                    <th class="py-2 pr-3">{{ t('admin.dataManagement.sourceProfiles.columns.actions') }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="profile in postgresProfiles" :key="`pg-${profile.profile_id}`" class="border-b border-gray-100 align-top dark:border-dark-800">
                    <td class="py-3 pl-3 pr-2">
                      <div class="font-mono text-xs">{{ profile.profile_id }}</div>
                      <div class="mt-1 text-xs text-gray-600 dark:text-gray-400">{{ profile.name }}</div>
                    </td>
                    <td class="py-3 pr-2">
                      <span
                        class="rounded px-2 py-0.5 text-xs"
                        :class="profile.is_active ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300' : 'bg-gray-100 text-gray-700 dark:bg-dark-800 dark:text-gray-300'"
                      >
                        {{ profile.is_active ? t('common.enabled') : t('common.disabled') }}
                      </span>
                    </td>
                    <td class="py-3 pr-2 text-xs">
                      {{ profile.config.host || '-' }}:{{ profile.config.port || '-' }}
                    </td>
                    <td class="py-3 pr-2 text-xs">
                      {{ profile.config.database || '-' }}
                    </td>
                    <td class="py-3 pr-2 text-xs">{{ formatDate(profile.updated_at) }}</td>
                    <td class="py-3 pr-3 text-xs">
                      <div class="flex flex-wrap gap-2">
                        <button type="button" class="btn btn-secondary btn-xs" :disabled="!health.enabled" @click="editSourceProfile('postgres', profile.profile_id)">
                          {{ t('common.edit') }}
                        </button>
                        <button
                          v-if="!profile.is_active"
                          type="button"
                          class="btn btn-secondary btn-xs"
                          :disabled="!health.enabled || activatingSourceProfile"
                          @click="activateSourceProfile('postgres', profile.profile_id)"
                        >
                          {{ t('admin.dataManagement.actions.activateProfile') }}
                        </button>
                        <button
                          v-if="!profile.is_active"
                          type="button"
                          class="btn btn-danger btn-xs"
                          :disabled="!health.enabled || deletingSourceProfile"
                          @click="removeSourceProfile('postgres', profile.profile_id)"
                        >
                          {{ t('common.delete') }}
                        </button>
                      </div>
                    </td>
                  </tr>
                  <tr v-if="postgresProfiles.length === 0">
                    <td colspan="6" class="py-6 text-center text-sm text-gray-500 dark:text-gray-400">
                      {{ t('admin.dataManagement.sourceProfiles.empty') }}
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>

          <div>
            <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
              <h4 class="text-sm font-semibold text-gray-900 dark:text-white">
                {{ t('admin.dataManagement.form.redis.title') }}
              </h4>
              <div class="flex gap-2">
                <button type="button" class="btn btn-secondary btn-xs" :disabled="!health.enabled" @click="startCreateSourceProfile('redis')">
                  {{ t('admin.dataManagement.actions.newSourceProfile') }}
                </button>
                <button type="button" class="btn btn-secondary btn-xs" :disabled="!health.enabled || loadingSourceProfiles" @click="loadSourceProfiles">
                  {{ loadingSourceProfiles ? t('common.loading') : t('admin.dataManagement.actions.reloadSourceProfiles') }}
                </button>
              </div>
            </div>

            <div class="overflow-x-auto rounded-lg border border-gray-200 dark:border-dark-700">
              <table class="w-full min-w-[680px] text-sm">
                <thead>
                  <tr class="border-b border-gray-200 bg-gray-50 text-left text-xs uppercase tracking-wide text-gray-500 dark:border-dark-700 dark:bg-dark-900 dark:text-gray-400">
                    <th class="py-2 pl-3 pr-2">{{ t('admin.dataManagement.sourceProfiles.columns.profile') }}</th>
                    <th class="py-2 pr-2">{{ t('admin.dataManagement.sourceProfiles.columns.active') }}</th>
                    <th class="py-2 pr-2">{{ t('admin.dataManagement.sourceProfiles.columns.connection') }}</th>
                    <th class="py-2 pr-2">{{ t('admin.dataManagement.sourceProfiles.columns.database') }}</th>
                    <th class="py-2 pr-2">{{ t('admin.dataManagement.sourceProfiles.columns.updatedAt') }}</th>
                    <th class="py-2 pr-3">{{ t('admin.dataManagement.sourceProfiles.columns.actions') }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="profile in redisProfiles" :key="`redis-${profile.profile_id}`" class="border-b border-gray-100 align-top dark:border-dark-800">
                    <td class="py-3 pl-3 pr-2">
                      <div class="font-mono text-xs">{{ profile.profile_id }}</div>
                      <div class="mt-1 text-xs text-gray-600 dark:text-gray-400">{{ profile.name }}</div>
                    </td>
                    <td class="py-3 pr-2">
                      <span
                        class="rounded px-2 py-0.5 text-xs"
                        :class="profile.is_active ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300' : 'bg-gray-100 text-gray-700 dark:bg-dark-800 dark:text-gray-300'"
                      >
                        {{ profile.is_active ? t('common.enabled') : t('common.disabled') }}
                      </span>
                    </td>
                    <td class="py-3 pr-2 text-xs">{{ profile.config.addr || '-' }}</td>
                    <td class="py-3 pr-2 text-xs">db={{ profile.config.db }}</td>
                    <td class="py-3 pr-2 text-xs">{{ formatDate(profile.updated_at) }}</td>
                    <td class="py-3 pr-3 text-xs">
                      <div class="flex flex-wrap gap-2">
                        <button type="button" class="btn btn-secondary btn-xs" :disabled="!health.enabled" @click="editSourceProfile('redis', profile.profile_id)">
                          {{ t('common.edit') }}
                        </button>
                        <button
                          v-if="!profile.is_active"
                          type="button"
                          class="btn btn-secondary btn-xs"
                          :disabled="!health.enabled || activatingSourceProfile"
                          @click="activateSourceProfile('redis', profile.profile_id)"
                        >
                          {{ t('admin.dataManagement.actions.activateProfile') }}
                        </button>
                        <button
                          v-if="!profile.is_active"
                          type="button"
                          class="btn btn-danger btn-xs"
                          :disabled="!health.enabled || deletingSourceProfile"
                          @click="removeSourceProfile('redis', profile.profile_id)"
                        >
                          {{ t('common.delete') }}
                        </button>
                      </div>
                    </td>
                  </tr>
                  <tr v-if="redisProfiles.length === 0">
                    <td colspan="6" class="py-6 text-center text-sm text-gray-500 dark:text-gray-400">
                      {{ t('admin.dataManagement.sourceProfiles.empty') }}
                    </td>
                  </tr>
                </tbody>
              </table>
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

      <div class="card p-6">
        <h3 class="text-base font-semibold text-gray-900 dark:text-white">
          {{ t('admin.dataManagement.sections.backup.title') }}
        </h3>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.dataManagement.sections.backup.description') }}
        </p>

        <div class="mt-4 grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-4">
          <select v-model="createForm.backup_type" class="input w-full" :disabled="!health.enabled">
            <option value="postgres">postgres</option>
            <option value="redis">redis</option>
            <option value="full">full</option>
          </select>

          <select
            v-model="createForm.postgres_profile_id"
            class="input w-full"
            :disabled="!health.enabled || (createForm.backup_type !== 'postgres' && createForm.backup_type !== 'full')"
          >
            <option value="">{{ t('admin.dataManagement.form.useActivePostgresProfile') }}</option>
            <option v-for="profile in postgresProfiles" :key="`backup-pg-${profile.profile_id}`" :value="profile.profile_id">
              {{ profile.profile_id }} · {{ profile.name }}
            </option>
          </select>

          <select
            v-model="createForm.redis_profile_id"
            class="input w-full"
            :disabled="!health.enabled || (createForm.backup_type !== 'redis' && createForm.backup_type !== 'full')"
          >
            <option value="">{{ t('admin.dataManagement.form.useActiveRedisProfile') }}</option>
            <option v-for="profile in redisProfiles" :key="`backup-redis-${profile.profile_id}`" :value="profile.profile_id">
              {{ profile.profile_id }} · {{ profile.name }}
            </option>
          </select>

          <label class="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
            <input v-model="createForm.upload_to_s3" type="checkbox" :disabled="!health.enabled" />
            <span>{{ t('admin.dataManagement.form.uploadToS3') }}</span>
          </label>

          <select v-model="createForm.s3_profile_id" class="input w-full" :disabled="!health.enabled || !createForm.upload_to_s3">
            <option value="">{{ t('admin.dataManagement.form.useActiveS3Profile') }}</option>
            <option v-for="profile in s3Profiles" :key="profile.profile_id" :value="profile.profile_id">
              {{ profile.profile_id }} · {{ profile.name }}
            </option>
          </select>

          <input
            v-model="createForm.idempotency_key"
            class="input w-full md:col-span-2 xl:col-span-3"
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
          <table class="w-full min-w-[1100px] text-sm">
            <thead>
              <tr class="border-b border-gray-200 text-left text-xs uppercase tracking-wide text-gray-500 dark:border-dark-700 dark:text-gray-400">
                <th class="py-2 pr-4">{{ t('admin.dataManagement.history.columns.jobID') }}</th>
                <th class="py-2 pr-4">{{ t('admin.dataManagement.history.columns.type') }}</th>
                <th class="py-2 pr-4">{{ t('admin.dataManagement.history.columns.status') }}</th>
                <th class="py-2 pr-4">{{ t('admin.dataManagement.history.columns.triggeredBy') }}</th>
                <th class="py-2 pr-4">{{ t('admin.dataManagement.history.columns.pgProfile') }}</th>
                <th class="py-2 pr-4">{{ t('admin.dataManagement.history.columns.redisProfile') }}</th>
                <th class="py-2 pr-4">{{ t('admin.dataManagement.history.columns.s3Profile') }}</th>
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
                <td class="py-3 pr-4 font-mono text-xs">{{ job.postgres_profile_id || '-' }}</td>
                <td class="py-3 pr-4 font-mono text-xs">{{ job.redis_profile_id || '-' }}</td>
                <td class="py-3 pr-4 font-mono text-xs">{{ job.s3_profile_id || '-' }}</td>
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
                <td colspan="10" class="py-6 text-center text-sm text-gray-500 dark:text-gray-400">
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

    <Teleport to="body">
      <Transition name="dm-drawer-mask">
        <div
          v-if="sourceDrawerOpen"
          class="fixed inset-0 z-50 bg-black/40 backdrop-blur-sm"
          @click="closeSourceDrawer"
        ></div>
      </Transition>

      <Transition name="dm-drawer-panel">
        <div
          v-if="sourceDrawerOpen"
          class="fixed inset-y-0 right-0 z-[51] flex h-full w-full max-w-2xl flex-col border-l border-gray-200 bg-white shadow-2xl dark:border-dark-700 dark:bg-dark-900"
        >
          <div class="flex items-center justify-between border-b border-gray-200 px-4 py-3 dark:border-dark-700">
            <h4 class="text-sm font-semibold text-gray-900 dark:text-white">
              {{ creatingSourceProfile ? t('admin.dataManagement.sourceProfiles.createTitle') : t('admin.dataManagement.sourceProfiles.editTitle') }}
              · {{ sourceDrawerTypeLabel }}
            </h4>
            <button
              type="button"
              class="rounded p-1 text-gray-500 hover:bg-gray-100 hover:text-gray-700 dark:text-gray-400 dark:hover:bg-dark-800 dark:hover:text-gray-200"
              @click="closeSourceDrawer"
            >
              ✕
            </button>
          </div>

          <div class="flex-1 overflow-y-auto p-4">
            <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
              <input
                v-model="sourceForm.profile_id"
                class="input w-full"
                :placeholder="t('admin.dataManagement.form.source.profileID')"
                :disabled="!health.enabled || !creatingSourceProfile"
              />
              <input
                v-model="sourceForm.name"
                class="input w-full"
                :placeholder="t('admin.dataManagement.form.source.profileName')"
                :disabled="!health.enabled"
              />

              <template v-if="sourceDrawerType === 'postgres'">
                <input v-model="sourceForm.host" class="input w-full" :placeholder="t('admin.dataManagement.form.postgres.host')" :disabled="!health.enabled" />
                <input v-model.number="sourceForm.port" type="number" class="input w-full" :placeholder="t('admin.dataManagement.form.postgres.port')" :disabled="!health.enabled" />
                <input v-model="sourceForm.user" class="input w-full" :placeholder="t('admin.dataManagement.form.postgres.user')" :disabled="!health.enabled" />
                <input
                  v-model="sourceForm.password"
                  type="password"
                  class="input w-full"
                  :placeholder="sourceForm.password_configured ? t('admin.dataManagement.form.secretConfigured') : t('admin.dataManagement.form.postgres.password')"
                  :disabled="!health.enabled"
                />
                <input v-model="sourceForm.database" class="input w-full" :placeholder="t('admin.dataManagement.form.postgres.database')" :disabled="!health.enabled" />
                <input v-model="sourceForm.ssl_mode" class="input w-full" :placeholder="t('admin.dataManagement.form.postgres.sslMode')" :disabled="!health.enabled" />
                <input v-model="sourceForm.container_name" class="input w-full md:col-span-2" :placeholder="t('admin.dataManagement.form.postgres.containerName')" :disabled="!health.enabled" />
              </template>

              <template v-else>
                <input v-model="sourceForm.addr" class="input w-full md:col-span-2" :placeholder="t('admin.dataManagement.form.redis.addr')" :disabled="!health.enabled" />
                <input v-model="sourceForm.username" class="input w-full" :placeholder="t('admin.dataManagement.form.redis.username')" :disabled="!health.enabled" />
                <input
                  v-model="sourceForm.password"
                  type="password"
                  class="input w-full"
                  :placeholder="sourceForm.password_configured ? t('admin.dataManagement.form.secretConfigured') : t('admin.dataManagement.form.redis.password')"
                  :disabled="!health.enabled"
                />
                <input v-model.number="sourceForm.db" type="number" class="input w-full" :placeholder="t('admin.dataManagement.form.redis.db')" :disabled="!health.enabled" />
                <input v-model="sourceForm.container_name" class="input w-full md:col-span-2" :placeholder="t('admin.dataManagement.form.redis.containerName')" :disabled="!health.enabled" />
              </template>

              <label v-if="creatingSourceProfile" class="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300 md:col-span-2">
                <input v-model="sourceForm.set_active" type="checkbox" :disabled="!health.enabled" />
                <span>{{ t('admin.dataManagement.form.source.setActive') }}</span>
              </label>
            </div>
          </div>

          <div class="flex flex-wrap justify-end gap-2 border-t border-gray-200 p-4 dark:border-dark-700">
            <button type="button" class="btn btn-secondary btn-sm" :disabled="!health.enabled" @click="closeSourceDrawer">
              {{ t('common.cancel') }}
            </button>
            <button type="button" class="btn btn-primary btn-sm" :disabled="!health.enabled || savingSourceProfile" @click="saveSourceProfile">
              {{ savingSourceProfile ? t('common.loading') : t('admin.dataManagement.actions.saveProfile') }}
            </button>
          </div>
        </div>
      </Transition>
    </Teleport>

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
  type BackupJob,
  type BackupJobStatus,
  type DataManagementConfig,
  type DataManagementS3Profile,
  type DataManagementSourceProfile,
  type SourceType
} from '@/api/admin/dataManagement'
import { useAppStore } from '@/stores'

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const loadingConfig = ref(false)
const savingConfig = ref(false)
const testingS3 = ref(false)
const loadingProfiles = ref(false)
const savingProfile = ref(false)
const activatingProfile = ref(false)
const deletingProfile = ref(false)
const creatingProfile = ref(false)
const profileDrawerOpen = ref(false)
const creatingBackup = ref(false)
const loadingJobs = ref(false)
const loadingSourceProfiles = ref(false)
const savingSourceProfile = ref(false)
const activatingSourceProfile = ref(false)
const deletingSourceProfile = ref(false)
const creatingSourceProfile = ref(false)
const sourceDrawerOpen = ref(false)

const health = ref<BackupAgentHealth>({
  enabled: false,
  reason: 'BACKUP_AGENT_SOCKET_MISSING',
  socket_path: '/tmp/sub2api-backup.sock'
})

const config = ref<DataManagementConfig>(newDefaultConfig())
const jobs = ref<BackupJob[]>([])
const nextPageToken = ref('')
const s3Profiles = ref<DataManagementS3Profile[]>([])
const selectedProfileID = ref('')
const postgresProfiles = ref<DataManagementSourceProfile[]>([])
const redisProfiles = ref<DataManagementSourceProfile[]>([])
const sourceDrawerType = ref<SourceType>('postgres')
const selectedSourceProfileID = ref('')

const createForm = ref({
  backup_type: 'full' as 'postgres' | 'redis' | 'full',
  upload_to_s3: true,
  s3_profile_id: '',
  postgres_profile_id: '',
  redis_profile_id: '',
  idempotency_key: ''
})

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

type SourceProfileForm = {
  profile_id: string
  name: string
  set_active: boolean
  host: string
  port: number
  user: string
  password: string
  password_configured: boolean
  database: string
  ssl_mode: string
  addr: string
  username: string
  db: number
  container_name: string
}

const profileForm = ref<S3ProfileForm>(newDefaultS3ProfileForm())
const sourceForm = ref<SourceProfileForm>(newDefaultSourceProfileForm())

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

const sourceDrawerTypeLabel = computed(() => {
  return sourceDrawerType.value === 'postgres'
    ? t('admin.dataManagement.form.postgres.title')
    : t('admin.dataManagement.form.redis.title')
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

    if (!createForm.value.postgres_profile_id) {
      createForm.value.postgres_profile_id = config.value.active_postgres_profile_id || ''
    }
    if (!createForm.value.redis_profile_id) {
      createForm.value.redis_profile_id = config.value.active_redis_profile_id || ''
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
    config.value = {
      ...config.value,
      ...updated,
      postgres: { ...config.value.postgres, ...(updated.postgres || {}) },
      redis: { ...config.value.redis, ...(updated.redis || {}) },
      s3: { ...config.value.s3, ...(updated.s3 || {}) }
    }
    await loadS3Profiles()
    appStore.showSuccess(t('admin.dataManagement.actions.configSaved'))
  } catch (error) {
    appStore.showError((error as { message?: string })?.message || t('errors.networkError'))
  } finally {
    savingConfig.value = false
  }
}

async function loadSourceProfiles() {
  if (!health.value.enabled) {
    return
  }
  loadingSourceProfiles.value = true
  try {
    const [pgResult, redisResult] = await Promise.all([
      dataManagementAPI.listSourceProfiles('postgres'),
      dataManagementAPI.listSourceProfiles('redis')
    ])
    postgresProfiles.value = pgResult.items || []
    redisProfiles.value = redisResult.items || []

    if (sourceDrawerOpen.value && !creatingSourceProfile.value) {
      syncSourceFormWithSelection()
    }

    if (createForm.value.postgres_profile_id) {
      const exists = postgresProfiles.value.some((item) => item.profile_id === createForm.value.postgres_profile_id)
      if (!exists) {
        createForm.value.postgres_profile_id = config.value.active_postgres_profile_id || ''
      }
    }
    if (createForm.value.redis_profile_id) {
      const exists = redisProfiles.value.some((item) => item.profile_id === createForm.value.redis_profile_id)
      if (!exists) {
        createForm.value.redis_profile_id = config.value.active_redis_profile_id || ''
      }
    }
  } catch (error) {
    appStore.showError((error as { message?: string })?.message || t('errors.networkError'))
  } finally {
    loadingSourceProfiles.value = false
  }
}

function startCreateSourceProfile(sourceType: SourceType) {
  creatingSourceProfile.value = true
  sourceDrawerType.value = sourceType
  selectedSourceProfileID.value = ''
  sourceForm.value = newDefaultSourceProfileForm(undefined, sourceType)
  sourceDrawerOpen.value = true
}

function editSourceProfile(sourceType: SourceType, profileID: string) {
  creatingSourceProfile.value = false
  sourceDrawerType.value = sourceType
  selectedSourceProfileID.value = profileID
  syncSourceFormWithSelection()
  sourceDrawerOpen.value = true
}

function closeSourceDrawer() {
  sourceDrawerOpen.value = false
  if (creatingSourceProfile.value) {
    creatingSourceProfile.value = false
    selectedSourceProfileID.value = ''
  }
}

function syncSourceFormWithSelection() {
  const targetProfiles = sourceDrawerType.value === 'postgres' ? postgresProfiles.value : redisProfiles.value
  const profile = targetProfiles.find((item) => item.profile_id === selectedSourceProfileID.value)
  sourceForm.value = newDefaultSourceProfileForm(profile, sourceDrawerType.value)
}

async function saveSourceProfile() {
  if (!health.value.enabled) {
    return
  }
  if (!sourceForm.value.name.trim()) {
    appStore.showError(t('admin.dataManagement.actions.profileNameRequired'))
    return
  }
  if (creatingSourceProfile.value && !sourceForm.value.profile_id.trim()) {
    appStore.showError(t('admin.dataManagement.actions.profileIDRequired'))
    return
  }
  if (!creatingSourceProfile.value && !selectedSourceProfileID.value) {
    appStore.showError(t('admin.dataManagement.actions.profileSelectRequired'))
    return
  }

  savingSourceProfile.value = true
  try {
    const payload = {
      name: sourceForm.value.name.trim(),
      config: {
        host: sourceForm.value.host,
        port: sourceForm.value.port,
        user: sourceForm.value.user,
        password: sourceForm.value.password || undefined,
        database: sourceForm.value.database,
        ssl_mode: sourceForm.value.ssl_mode,
        addr: sourceForm.value.addr,
        username: sourceForm.value.username,
        db: sourceForm.value.db,
        container_name: sourceForm.value.container_name
      }
    }

    if (creatingSourceProfile.value) {
      const created = await dataManagementAPI.createSourceProfile(sourceDrawerType.value, {
        profile_id: sourceForm.value.profile_id.trim(),
        set_active: sourceForm.value.set_active,
        ...payload
      })
      selectedSourceProfileID.value = created.profile_id
      creatingSourceProfile.value = false
      sourceDrawerOpen.value = false
      appStore.showSuccess(t('admin.dataManagement.actions.sourceProfileCreated'))
    } else {
      await dataManagementAPI.updateSourceProfile(
        sourceDrawerType.value,
        selectedSourceProfileID.value,
        payload
      )
      sourceDrawerOpen.value = false
      appStore.showSuccess(t('admin.dataManagement.actions.sourceProfileSaved'))
    }

    await Promise.all([loadConfig(), loadSourceProfiles()])
  } catch (error) {
    appStore.showError((error as { message?: string })?.message || t('errors.networkError'))
  } finally {
    savingSourceProfile.value = false
  }
}

async function activateSourceProfile(sourceType: SourceType, profileID: string) {
  if (!health.value.enabled) {
    return
  }
  activatingSourceProfile.value = true
  try {
    await dataManagementAPI.setActiveSourceProfile(sourceType, profileID)
    appStore.showSuccess(t('admin.dataManagement.actions.sourceProfileActivated'))
    await Promise.all([loadConfig(), loadSourceProfiles()])
  } catch (error) {
    appStore.showError((error as { message?: string })?.message || t('errors.networkError'))
  } finally {
    activatingSourceProfile.value = false
  }
}

async function removeSourceProfile(sourceType: SourceType, profileID: string) {
  if (!health.value.enabled) {
    return
  }
  if (!window.confirm(t('admin.dataManagement.sourceProfiles.deleteConfirm', { profileID }))) {
    return
  }

  deletingSourceProfile.value = true
  try {
    await dataManagementAPI.deleteSourceProfile(sourceType, profileID)
    if (selectedSourceProfileID.value === profileID) {
      selectedSourceProfileID.value = ''
    }
    appStore.showSuccess(t('admin.dataManagement.actions.sourceProfileDeleted'))
    await Promise.all([loadConfig(), loadSourceProfiles()])
  } catch (error) {
    appStore.showError((error as { message?: string })?.message || t('errors.networkError'))
  } finally {
    deletingSourceProfile.value = false
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

    if (createForm.value.s3_profile_id) {
      const selectable = s3Profiles.value.some((item) => item.profile_id === createForm.value.s3_profile_id)
      if (!selectable) {
        createForm.value.s3_profile_id = ''
      }
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

    await Promise.all([loadConfig(), loadS3Profiles()])
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
    await Promise.all([loadConfig(), loadS3Profiles()])
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
    await Promise.all([loadConfig(), loadS3Profiles()])
  } catch (error) {
    appStore.showError((error as { message?: string })?.message || t('errors.networkError'))
  } finally {
    deletingProfile.value = false
  }
}

async function createBackup() {
  if (!health.value.enabled) {
    return
  }
  creatingBackup.value = true
  try {
    const needPostgres = createForm.value.backup_type === 'postgres' || createForm.value.backup_type === 'full'
    const needRedis = createForm.value.backup_type === 'redis' || createForm.value.backup_type === 'full'

    const result = await dataManagementAPI.createBackupJob({
      backup_type: createForm.value.backup_type,
      upload_to_s3: createForm.value.upload_to_s3,
      s3_profile_id:
        createForm.value.upload_to_s3 && createForm.value.s3_profile_id
          ? createForm.value.s3_profile_id
          : undefined,
      postgres_profile_id:
        needPostgres && createForm.value.postgres_profile_id
          ? createForm.value.postgres_profile_id
          : undefined,
      redis_profile_id:
        needRedis && createForm.value.redis_profile_id
          ? createForm.value.redis_profile_id
          : undefined,
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

function newDefaultConfig(): DataManagementConfig {
  return {
    source_mode: 'direct',
    backup_root: '/var/lib/sub2api/backups',
    retention_days: 7,
    keep_last: 30,
    active_postgres_profile_id: '',
    active_redis_profile_id: '',
    active_s3_profile_id: '',
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

function newDefaultSourceProfileForm(profile?: DataManagementSourceProfile, sourceType: SourceType = 'postgres'): SourceProfileForm {
  if (!profile) {
    return {
      profile_id: '',
      name: '',
      set_active: false,
      host: sourceType === 'postgres' ? '127.0.0.1' : '',
      port: sourceType === 'postgres' ? 5432 : 0,
      user: sourceType === 'postgres' ? 'postgres' : '',
      password: '',
      password_configured: false,
      database: sourceType === 'postgres' ? 'sub2api' : '',
      ssl_mode: sourceType === 'postgres' ? 'disable' : '',
      addr: sourceType === 'redis' ? '127.0.0.1:6379' : '',
      username: '',
      db: 0,
      container_name: ''
    }
  }

  return {
    profile_id: profile.profile_id,
    name: profile.name,
    set_active: false,
    host: profile.config.host || '',
    port: profile.config.port || 0,
    user: profile.config.user || '',
    password: '',
    password_configured: Boolean(profile.password_configured),
    database: profile.config.database || '',
    ssl_mode: profile.config.ssl_mode || '',
    addr: profile.config.addr || '',
    username: profile.config.username || '',
    db: profile.config.db || 0,
    container_name: profile.config.container_name || ''
  }
}

onMounted(async () => {
  await loadAgentHealth()
  if (health.value.enabled) {
    await Promise.all([loadConfig(), loadSourceProfiles(), loadS3Profiles(), refreshJobs()])
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
