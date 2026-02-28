<template>
  <BaseDialog
    :show="show"
    :title="scopedDialogTitle"
    width="wide"
    @close="handleClose"
  >
    <form id="bulk-edit-account-form" class="space-y-5" @submit.prevent="handleSubmit">
      <!-- Info -->
      <div class="rounded-lg bg-blue-50 p-4 dark:bg-blue-900/20">
        <p class="text-sm text-blue-700 dark:text-blue-400">
          <svg class="mr-1.5 inline h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
            />
          </svg>
          {{ t('admin.accounts.bulkEdit.selectionInfo', { count: accountIds.length }) }}
        </p>
      </div>

      <div class="rounded-lg border border-gray-200 bg-white px-4 py-3 text-sm text-gray-600 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300">
        {{ t('admin.accounts.filters.platform') }}: {{ scopePlatformLabel }} /
        {{ t('admin.accounts.filters.type') }}: {{ scopeTypeLabel }}
      </div>

      <div v-if="hasTemplateScope" class="rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-600 dark:bg-dark-800">
        <div class="mb-3 flex items-start justify-between gap-3">
          <div>
            <p class="text-sm font-medium text-gray-900 dark:text-gray-100">
              {{ t('admin.accounts.bulkEdit.templateTitle') }}
            </p>
            <p class="text-xs text-gray-500 dark:text-gray-400">
              {{
                t('admin.accounts.bulkEdit.templateScopeHint', {
                  platform: scopePlatformLabel,
                  type: scopeTypeLabel
                })
              }}
            </p>
            <p v-if="templateLoading" class="mt-1 text-xs text-primary-600 dark:text-primary-400">
              {{ t('admin.accounts.bulkEdit.templateLoading') }}
            </p>
          </div>
          <span
            class="inline-flex min-w-[1.5rem] items-center justify-center rounded bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-600 dark:bg-dark-700 dark:text-gray-300"
          >
            {{ scopedTemplateRecords.length }}
          </span>
        </div>

        <div class="grid grid-cols-1 gap-3 md:grid-cols-[minmax(0,1fr)_auto_auto]">
          <div>
            <label class="input-label mb-2">{{ t('admin.accounts.bulkEdit.templateSelectLabel') }}</label>
            <Select
              v-model="selectedTemplateId"
              :options="templateOptions"
              :placeholder="t('admin.accounts.bulkEdit.templateEmpty')"
            />
          </div>
          <button
            type="button"
            class="btn btn-secondary mt-auto"
            :disabled="!canApplySelectedTemplate"
            @click="applySelectedTemplate"
          >
            {{ t('admin.accounts.bulkEdit.templateApply') }}
          </button>
          <button
            type="button"
            class="btn btn-secondary mt-auto"
            :disabled="!canDeleteSelectedTemplate"
            @click="removeSelectedTemplate"
          >
            {{ t('admin.accounts.bulkEdit.templateDelete') }}
          </button>
        </div>

        <div class="mt-3 grid grid-cols-1 gap-3 md:grid-cols-[minmax(0,1fr)_auto]">
          <div>
            <label class="input-label mb-2">{{ t('admin.accounts.bulkEdit.templateNameLabel') }}</label>
            <input
              v-model="templateName"
              type="text"
              class="input"
              :placeholder="t('admin.accounts.bulkEdit.templateNamePlaceholder')"
              @keyup.enter="saveTemplate"
            />
          </div>
          <button
            type="button"
            class="btn btn-primary mt-auto"
            :disabled="!canSaveTemplate || templateLoading"
            @click="saveTemplate"
          >
            {{ t('admin.accounts.bulkEdit.templateSave') }}
          </button>
        </div>

        <div class="mt-3 grid grid-cols-1 gap-3 md:grid-cols-2">
          <div>
            <label class="input-label mb-2">
              {{ t('admin.accounts.bulkEdit.templateShareScopeLabel') }}
            </label>
            <Select v-model="templateShareScope" :options="templateShareScopeOptions" />
          </div>
          <div v-if="templateShareScope === 'groups'" class="md:col-span-2">
            <label class="input-label mb-2">
              {{ t('admin.accounts.bulkEdit.templateShareGroupsLabel') }}
            </label>
            <GroupSelector v-model="templateShareGroupIds" :groups="groups" />
            <p class="input-hint">
              {{ t('admin.accounts.bulkEdit.templateShareGroupsHint') }}
            </p>
          </div>
        </div>

        <div v-if="selectedTemplateRecord" class="mt-4 rounded-lg border border-gray-200 p-3 dark:border-dark-600">
          <div class="mb-2 flex items-start justify-between gap-2">
            <div>
              <p class="text-sm font-medium text-gray-900 dark:text-gray-100">
                {{ t('admin.accounts.bulkEdit.templateVersionTitle') }}
              </p>
              <p class="text-xs text-gray-500 dark:text-gray-400">
                {{ t('admin.accounts.bulkEdit.templateVersionHint') }}
              </p>
            </div>
            <span
              class="inline-flex min-w-[1.5rem] items-center justify-center rounded bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-600 dark:bg-dark-700 dark:text-gray-300"
            >
              {{ templateVersionRecords.length }}
            </span>
          </div>

          <p v-if="templateVersionLoading" class="text-xs text-primary-600 dark:text-primary-400">
            {{ t('admin.accounts.bulkEdit.templateVersionLoading') }}
          </p>

          <p
            v-else-if="templateVersionRecords.length === 0"
            class="rounded bg-gray-50 px-3 py-2 text-xs text-gray-500 dark:bg-dark-700 dark:text-gray-400"
          >
            {{ t('admin.accounts.bulkEdit.templateVersionEmpty') }}
          </p>

          <ul v-else class="space-y-2">
            <li
              v-for="version in templateVersionRecords"
              :key="version.versionID"
              class="flex items-center justify-between gap-2 rounded bg-gray-50 px-3 py-2 dark:bg-dark-700"
            >
              <div class="min-w-0">
                <p class="truncate text-xs font-medium text-gray-700 dark:text-gray-200">
                  {{ formatTemplateVersionUpdatedAt(version.updatedAt) }}
                </p>
                <p class="truncate text-[11px] text-gray-500 dark:text-gray-400">
                  {{ resolveTemplateShareScopeLabel(version.shareScope) }}
                  <span v-if="version.groupIDs.length > 0"> · {{ version.groupIDs.join(', ') }}</span>
                </p>
              </div>
              <button
                type="button"
                class="btn btn-secondary btn-xs"
                :disabled="templateRollbackingVersionID === version.versionID"
                @click="rollbackTemplateVersion(version.versionID)"
              >
                {{
                  templateRollbackingVersionID === version.versionID
                    ? t('admin.accounts.bulkEdit.templateRollbacking')
                    : t('admin.accounts.bulkEdit.templateRollback')
                }}
              </button>
            </li>
          </ul>
        </div>
      </div>

      <div
        v-if="supportsOpenAIPassthrough || supportsOpenAIWSMode || supportsCodexCLIOnly || supportsAnthropicPassthrough"
        class="space-y-4 border-t border-gray-200 pt-4 dark:border-dark-600"
      >
        <div v-if="supportsOpenAIPassthrough" class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
          <div class="mb-3 flex items-center justify-between">
            <label class="input-label mb-0" for="bulk-edit-openai-passthrough-enabled">
              {{ t('admin.accounts.openai.oauthPassthrough') }}
            </label>
            <input
              v-model="enableOpenAIPassthrough"
              id="bulk-edit-openai-passthrough-enabled"
              type="checkbox"
              class="rounded border-gray-300 text-primary-600 focus:ring-primary-500"
            />
          </div>
          <div :class="!enableOpenAIPassthrough && 'pointer-events-none opacity-50'">
            <button
              type="button"
              :class="[
                'relative inline-flex h-6 w-11 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2',
                openAIPassthroughEnabled ? 'bg-primary-600' : 'bg-gray-200 dark:bg-dark-600'
              ]"
              @click="openAIPassthroughEnabled = !openAIPassthroughEnabled"
            >
              <span
                :class="[
                  'pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out',
                  openAIPassthroughEnabled ? 'translate-x-5' : 'translate-x-0'
                ]"
              />
            </button>
          </div>
        </div>

        <div v-if="supportsOpenAIWSMode" class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
          <div class="mb-3 flex items-center justify-between">
            <label class="input-label mb-0" for="bulk-edit-openai-ws-mode-enabled">
              {{ t('admin.accounts.openai.wsMode') }}
            </label>
            <input
              v-model="enableOpenAIWSMode"
              id="bulk-edit-openai-ws-mode-enabled"
              type="checkbox"
              class="rounded border-gray-300 text-primary-600 focus:ring-primary-500"
            />
          </div>
          <div :class="!enableOpenAIWSMode && 'pointer-events-none opacity-50'">
            <Select v-model="openAIWSMode" :options="openAIWSModeOptions" />
          </div>
        </div>

        <div v-if="supportsCodexCLIOnly" class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
          <div class="mb-3 flex items-center justify-between">
            <label class="input-label mb-0" for="bulk-edit-codex-cli-only-enabled">
              {{ t('admin.accounts.openai.codexCLIOnly') }}
            </label>
            <input
              v-model="enableCodexCLIOnly"
              id="bulk-edit-codex-cli-only-enabled"
              type="checkbox"
              class="rounded border-gray-300 text-primary-600 focus:ring-primary-500"
            />
          </div>
          <div :class="!enableCodexCLIOnly && 'pointer-events-none opacity-50'">
            <button
              type="button"
              :class="[
                'relative inline-flex h-6 w-11 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2',
                codexCLIOnlyEnabled ? 'bg-primary-600' : 'bg-gray-200 dark:bg-dark-600'
              ]"
              @click="codexCLIOnlyEnabled = !codexCLIOnlyEnabled"
            >
              <span
                :class="[
                  'pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out',
                  codexCLIOnlyEnabled ? 'translate-x-5' : 'translate-x-0'
                ]"
              />
            </button>
          </div>
        </div>

        <div v-if="supportsAnthropicPassthrough" class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
          <div class="mb-3 flex items-center justify-between">
            <label class="input-label mb-0" for="bulk-edit-anthropic-passthrough-enabled">
              {{ t('admin.accounts.anthropic.apiKeyPassthrough') }}
            </label>
            <input
              v-model="enableAnthropicPassthrough"
              id="bulk-edit-anthropic-passthrough-enabled"
              type="checkbox"
              class="rounded border-gray-300 text-primary-600 focus:ring-primary-500"
            />
          </div>
          <div :class="!enableAnthropicPassthrough && 'pointer-events-none opacity-50'">
            <button
              type="button"
              :class="[
                'relative inline-flex h-6 w-11 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2',
                anthropicPassthroughEnabled ? 'bg-primary-600' : 'bg-gray-200 dark:bg-dark-600'
              ]"
              @click="anthropicPassthroughEnabled = !anthropicPassthroughEnabled"
            >
              <span
                :class="[
                  'pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out',
                  anthropicPassthroughEnabled ? 'translate-x-5' : 'translate-x-0'
                ]"
              />
            </button>
          </div>
        </div>
      </div>

      <!-- Base URL (API Key only) -->
      <div v-if="supportsBaseUrl" class="border-t border-gray-200 pt-4 dark:border-dark-600">
        <div class="mb-3 flex items-center justify-between">
          <label
            id="bulk-edit-base-url-label"
            class="input-label mb-0"
            for="bulk-edit-base-url-enabled"
          >
            {{ t('admin.accounts.baseUrl') }}
          </label>
          <input
            v-model="enableBaseUrl"
            id="bulk-edit-base-url-enabled"
            type="checkbox"
            aria-controls="bulk-edit-base-url"
            class="rounded border-gray-300 text-primary-600 focus:ring-primary-500"
          />
        </div>
        <input
          v-model="baseUrl"
          id="bulk-edit-base-url"
          type="text"
          :disabled="!enableBaseUrl"
          class="input"
          :class="!enableBaseUrl && 'cursor-not-allowed opacity-50'"
          :placeholder="t('admin.accounts.bulkEdit.baseUrlPlaceholder')"
          aria-labelledby="bulk-edit-base-url-label"
        />
        <p class="input-hint">
          {{ t('admin.accounts.bulkEdit.baseUrlNotice') }}
        </p>
      </div>

      <!-- Model restriction -->
      <div v-if="supportsModelRestriction" class="border-t border-gray-200 pt-4 dark:border-dark-600">
        <div class="mb-3 flex items-center justify-between">
          <label
            id="bulk-edit-model-restriction-label"
            class="input-label mb-0"
            for="bulk-edit-model-restriction-enabled"
          >
            {{ t('admin.accounts.modelRestriction') }}
          </label>
          <input
            v-model="enableModelRestriction"
            id="bulk-edit-model-restriction-enabled"
            type="checkbox"
            aria-controls="bulk-edit-model-restriction-body"
            class="rounded border-gray-300 text-primary-600 focus:ring-primary-500"
          />
        </div>

        <div
          id="bulk-edit-model-restriction-body"
          :class="!enableModelRestriction && 'pointer-events-none opacity-50'"
          role="group"
          aria-labelledby="bulk-edit-model-restriction-label"
        >
          <!-- Mode Toggle -->
          <div class="mb-4 flex gap-2">
            <button
              type="button"
              :class="[
                'flex-1 rounded-lg px-4 py-2 text-sm font-medium transition-all',
                modelRestrictionMode === 'whitelist'
                  ? 'bg-primary-100 text-primary-700 dark:bg-primary-900/30 dark:text-primary-400'
                  : 'bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-dark-600 dark:text-gray-400 dark:hover:bg-dark-500'
              ]"
              @click="modelRestrictionMode = 'whitelist'"
            >
              <svg
                class="mr-1.5 inline h-4 w-4"
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
              >
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  stroke-width="2"
                  d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z"
                />
              </svg>
              {{ t('admin.accounts.modelWhitelist') }}
            </button>
            <button
              type="button"
              :class="[
                'flex-1 rounded-lg px-4 py-2 text-sm font-medium transition-all',
                modelRestrictionMode === 'mapping'
                  ? 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-400'
                  : 'bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-dark-600 dark:text-gray-400 dark:hover:bg-dark-500'
              ]"
              @click="modelRestrictionMode = 'mapping'"
            >
              <svg
                class="mr-1.5 inline h-4 w-4"
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
              >
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  stroke-width="2"
                  d="M8 7h12m0 0l-4-4m4 4l-4 4m0 6H4m0 0l4 4m-4-4l4-4"
                />
              </svg>
              {{ t('admin.accounts.modelMapping') }}
            </button>
          </div>

          <!-- Whitelist Mode -->
          <div v-if="modelRestrictionMode === 'whitelist'">
            <div class="mb-3 rounded-lg bg-blue-50 p-3 dark:bg-blue-900/20">
              <p class="text-xs text-blue-700 dark:text-blue-400">
                <svg
                  class="mr-1 inline h-4 w-4"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
                  />
                </svg>
                {{ t('admin.accounts.selectAllowedModels') }}
              </p>
            </div>

            <!-- Model Checkbox List -->
            <div class="mb-3 grid grid-cols-2 gap-2">
              <label
                v-for="model in filteredModels"
                :key="model.value"
                class="flex cursor-pointer items-center rounded-lg border p-3 transition-all hover:bg-gray-50 dark:border-dark-600 dark:hover:bg-dark-700"
                :class="
                  allowedModels.includes(model.value)
                    ? 'border-primary-500 bg-primary-50 dark:bg-primary-900/20'
                    : 'border-gray-200'
                "
              >
                <input
                  v-model="allowedModels"
                  type="checkbox"
                  :value="model.value"
                  class="mr-2 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
                />
                <span class="text-sm text-gray-700 dark:text-gray-300">{{ model.label }}</span>
              </label>
            </div>

            <p class="text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.accounts.selectedModels', { count: allowedModels.length }) }}
              <span v-if="allowedModels.length === 0">{{
                t('admin.accounts.supportsAllModels')
              }}</span>
            </p>
          </div>

          <!-- Mapping Mode -->
          <div v-else>
            <div class="mb-3 rounded-lg bg-purple-50 p-3 dark:bg-purple-900/20">
              <p class="text-xs text-purple-700 dark:text-purple-400">
                <svg
                  class="mr-1 inline h-4 w-4"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
                  />
                </svg>
                {{ t('admin.accounts.mapRequestModels') }}
              </p>
            </div>

            <!-- Model Mapping List -->
            <div v-if="modelMappings.length > 0" class="mb-3 space-y-2">
              <div
                v-for="(mapping, index) in modelMappings"
                :key="index"
                class="flex items-center gap-2"
              >
                <input
                  v-model="mapping.from"
                  type="text"
                  class="input flex-1"
                  :placeholder="t('admin.accounts.requestModel')"
                />
                <svg
                  class="h-4 w-4 flex-shrink-0 text-gray-400"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M14 5l7 7m0 0l-7 7m7-7H3"
                  />
                </svg>
                <input
                  v-model="mapping.to"
                  type="text"
                  class="input flex-1"
                  :placeholder="t('admin.accounts.actualModel')"
                />
                <button
                  type="button"
                  class="rounded-lg p-2 text-red-500 transition-colors hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20"
                  @click="removeModelMapping(index)"
                >
                  <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                    <path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      stroke-width="2"
                      d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
                    />
                  </svg>
                </button>
              </div>
            </div>

            <button
              type="button"
              class="mb-3 w-full rounded-lg border-2 border-dashed border-gray-300 px-4 py-2 text-gray-600 transition-colors hover:border-gray-400 hover:text-gray-700 dark:border-dark-500 dark:text-gray-400 dark:hover:border-dark-400 dark:hover:text-gray-300"
              @click="addModelMapping"
            >
              <svg
                class="mr-1 inline h-4 w-4"
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
              >
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  stroke-width="2"
                  d="M12 4v16m8-8H4"
                />
              </svg>
              {{ t('admin.accounts.addMapping') }}
            </button>

            <!-- Quick Add Buttons -->
            <div class="flex flex-wrap gap-2">
              <button
                v-for="preset in filteredPresets"
                :key="preset.label"
                type="button"
                :class="['rounded-lg px-3 py-1 text-xs transition-colors', preset.color]"
                @click="addPresetMapping(preset.from, preset.to)"
              >
                + {{ preset.label }}
              </button>
            </div>
          </div>
        </div>
      </div>

      <!-- Custom error codes -->
      <div v-if="supportsCustomErrorCodes" class="border-t border-gray-200 pt-4 dark:border-dark-600">
        <div class="mb-3 flex items-center justify-between">
          <div>
            <label
              id="bulk-edit-custom-error-codes-label"
              class="input-label mb-0"
              for="bulk-edit-custom-error-codes-enabled"
            >
              {{ t('admin.accounts.customErrorCodes') }}
            </label>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.accounts.customErrorCodesHint') }}
            </p>
          </div>
          <input
            v-model="enableCustomErrorCodes"
            id="bulk-edit-custom-error-codes-enabled"
            type="checkbox"
            aria-controls="bulk-edit-custom-error-codes-body"
            class="rounded border-gray-300 text-primary-600 focus:ring-primary-500"
          />
        </div>

        <div v-if="enableCustomErrorCodes" id="bulk-edit-custom-error-codes-body" class="space-y-3">
          <div class="rounded-lg bg-amber-50 p-3 dark:bg-amber-900/20">
            <p class="text-xs text-amber-700 dark:text-amber-400">
              <Icon name="exclamationTriangle" size="sm" class="mr-1 inline" :stroke-width="2" />
              {{ t('admin.accounts.customErrorCodesWarning') }}
            </p>
          </div>

          <!-- Error Code Buttons -->
          <div class="flex flex-wrap gap-2">
            <button
              v-for="code in commonErrorCodes"
              :key="code.value"
              type="button"
              :class="[
                'rounded-lg px-3 py-1.5 text-sm font-medium transition-colors',
                selectedErrorCodes.includes(code.value)
                  ? 'bg-red-100 text-red-700 ring-1 ring-red-500 dark:bg-red-900/30 dark:text-red-400'
                  : 'bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-dark-600 dark:text-gray-400 dark:hover:bg-dark-500'
              ]"
              @click="toggleErrorCode(code.value)"
            >
              {{ code.value }} {{ code.label }}
            </button>
          </div>

          <!-- Manual input -->
          <div class="flex items-center gap-2">
            <input
              v-model="customErrorCodeInput"
              id="bulk-edit-custom-error-code-input"
              type="number"
              min="100"
              max="599"
              class="input flex-1"
              :placeholder="t('admin.accounts.enterErrorCode')"
              aria-labelledby="bulk-edit-custom-error-codes-label"
              @keyup.enter="addCustomErrorCode"
            />
            <button type="button" class="btn btn-secondary px-3" @click="addCustomErrorCode">
              <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  stroke-width="2"
                  d="M12 4v16m8-8H4"
                />
              </svg>
            </button>
          </div>

          <!-- Selected codes summary -->
          <div class="flex flex-wrap gap-1.5">
            <span
              v-for="code in selectedErrorCodes.sort((a, b) => a - b)"
              :key="code"
              class="inline-flex items-center gap-1 rounded-full bg-red-100 px-2.5 py-0.5 text-sm font-medium text-red-700 dark:bg-red-900/30 dark:text-red-400"
            >
              {{ code }}
              <button
                type="button"
                class="hover:text-red-900 dark:hover:text-red-300"
                @click="removeErrorCode(code)"
              >
                <Icon name="x" size="xs" class="h-3.5 w-3.5" :stroke-width="2" />
              </button>
            </span>
            <span v-if="selectedErrorCodes.length === 0" class="text-xs text-gray-400">
              {{ t('admin.accounts.noneSelectedUsesDefault') }}
            </span>
          </div>
        </div>
      </div>

      <!-- Intercept warmup requests (Anthropic only) -->
      <div v-if="supportsInterceptWarmup" class="border-t border-gray-200 pt-4 dark:border-dark-600">
        <div class="flex items-center justify-between">
          <div class="flex-1 pr-4">
            <label
              id="bulk-edit-intercept-warmup-label"
              class="input-label mb-0"
              for="bulk-edit-intercept-warmup-enabled"
            >
              {{ t('admin.accounts.interceptWarmupRequests') }}
            </label>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.accounts.interceptWarmupRequestsDesc') }}
            </p>
          </div>
          <input
            v-model="enableInterceptWarmup"
            id="bulk-edit-intercept-warmup-enabled"
            type="checkbox"
            aria-controls="bulk-edit-intercept-warmup-body"
            class="rounded border-gray-300 text-primary-600 focus:ring-primary-500"
          />
        </div>
        <div v-if="enableInterceptWarmup" id="bulk-edit-intercept-warmup-body" class="mt-3">
          <button
            type="button"
            :class="[
              'relative inline-flex h-6 w-11 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2',
              interceptWarmupRequests ? 'bg-primary-600' : 'bg-gray-200 dark:bg-dark-600'
            ]"
            @click="interceptWarmupRequests = !interceptWarmupRequests"
          >
            <span
              :class="[
                'pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out',
                interceptWarmupRequests ? 'translate-x-5' : 'translate-x-0'
              ]"
            />
          </button>
        </div>
      </div>

      <!-- Proxy -->
      <div class="border-t border-gray-200 pt-4 dark:border-dark-600">
        <div class="mb-3 flex items-center justify-between">
          <label
            id="bulk-edit-proxy-label"
            class="input-label mb-0"
            for="bulk-edit-proxy-enabled"
          >
            {{ t('admin.accounts.proxy') }}
          </label>
          <input
            v-model="enableProxy"
            id="bulk-edit-proxy-enabled"
            type="checkbox"
            aria-controls="bulk-edit-proxy-body"
            class="rounded border-gray-300 text-primary-600 focus:ring-primary-500"
          />
        </div>
        <div id="bulk-edit-proxy-body" :class="!enableProxy && 'pointer-events-none opacity-50'">
          <ProxySelector
            v-model="proxyId"
            :proxies="proxies"
            aria-labelledby="bulk-edit-proxy-label"
          />
        </div>
      </div>

      <!-- Concurrency & Priority -->
      <div class="grid grid-cols-2 gap-4 border-t border-gray-200 pt-4 dark:border-dark-600 lg:grid-cols-3">
        <div>
          <div class="mb-3 flex items-center justify-between">
            <label
              id="bulk-edit-concurrency-label"
              class="input-label mb-0"
              for="bulk-edit-concurrency-enabled"
            >
              {{ t('admin.accounts.concurrency') }}
            </label>
            <input
              v-model="enableConcurrency"
              id="bulk-edit-concurrency-enabled"
              type="checkbox"
              aria-controls="bulk-edit-concurrency"
              class="rounded border-gray-300 text-primary-600 focus:ring-primary-500"
            />
          </div>
          <input
            v-model.number="concurrency"
            id="bulk-edit-concurrency"
            type="number"
            min="1"
            :disabled="!enableConcurrency"
            class="input"
            :class="!enableConcurrency && 'cursor-not-allowed opacity-50'"
            aria-labelledby="bulk-edit-concurrency-label"
          />
        </div>
        <div>
          <div class="mb-3 flex items-center justify-between">
            <label
              id="bulk-edit-priority-label"
              class="input-label mb-0"
              for="bulk-edit-priority-enabled"
            >
              {{ t('admin.accounts.priority') }}
            </label>
            <input
              v-model="enablePriority"
              id="bulk-edit-priority-enabled"
              type="checkbox"
              aria-controls="bulk-edit-priority"
              class="rounded border-gray-300 text-primary-600 focus:ring-primary-500"
            />
          </div>
          <input
            v-model.number="priority"
            id="bulk-edit-priority"
            type="number"
            min="1"
            :disabled="!enablePriority"
            class="input"
            :class="!enablePriority && 'cursor-not-allowed opacity-50'"
            aria-labelledby="bulk-edit-priority-label"
          />
        </div>
        <div>
          <div class="mb-3 flex items-center justify-between">
            <label
              id="bulk-edit-rate-multiplier-label"
              class="input-label mb-0"
              for="bulk-edit-rate-multiplier-enabled"
            >
              {{ t('admin.accounts.billingRateMultiplier') }}
            </label>
            <input
              v-model="enableRateMultiplier"
              id="bulk-edit-rate-multiplier-enabled"
              type="checkbox"
              aria-controls="bulk-edit-rate-multiplier"
              class="rounded border-gray-300 text-primary-600 focus:ring-primary-500"
            />
          </div>
          <input
            v-model.number="rateMultiplier"
            id="bulk-edit-rate-multiplier"
            type="number"
            min="0"
            step="0.01"
            :disabled="!enableRateMultiplier"
            class="input"
            :class="!enableRateMultiplier && 'cursor-not-allowed opacity-50'"
            aria-labelledby="bulk-edit-rate-multiplier-label"
          />
          <p class="input-hint">{{ t('admin.accounts.billingRateMultiplierHint') }}</p>
        </div>
      </div>

      <!-- Status -->
      <div class="border-t border-gray-200 pt-4 dark:border-dark-600">
        <div class="mb-3 flex items-center justify-between">
          <label
            id="bulk-edit-status-label"
            class="input-label mb-0"
            for="bulk-edit-status-enabled"
          >
            {{ t('common.status') }}
          </label>
          <input
            v-model="enableStatus"
            id="bulk-edit-status-enabled"
            type="checkbox"
            aria-controls="bulk-edit-status"
            class="rounded border-gray-300 text-primary-600 focus:ring-primary-500"
          />
        </div>
        <div id="bulk-edit-status" :class="!enableStatus && 'pointer-events-none opacity-50'">
          <Select
            v-model="status"
            :options="statusOptions"
            aria-labelledby="bulk-edit-status-label"
          />
        </div>
      </div>

      <!-- Groups -->
      <div class="border-t border-gray-200 pt-4 dark:border-dark-600">
        <div class="mb-3 flex items-center justify-between">
          <label
            id="bulk-edit-groups-label"
            class="input-label mb-0"
            for="bulk-edit-groups-enabled"
          >
            {{ t('nav.groups') }}
          </label>
          <input
            v-model="enableGroups"
            id="bulk-edit-groups-enabled"
            type="checkbox"
            aria-controls="bulk-edit-groups"
            class="rounded border-gray-300 text-primary-600 focus:ring-primary-500"
          />
        </div>
        <div id="bulk-edit-groups" :class="!enableGroups && 'pointer-events-none opacity-50'">
          <GroupSelector
            v-model="groupIds"
            :groups="groups"
            aria-labelledby="bulk-edit-groups-label"
          />
        </div>
      </div>
    </form>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="handleClose">
          {{ t('common.cancel') }}
        </button>
        <button
          type="submit"
          form="bulk-edit-account-form"
          :disabled="submitting"
          class="btn btn-primary"
        >
          <svg
            v-if="submitting"
            class="-ml-1 mr-2 h-4 w-4 animate-spin"
            fill="none"
            viewBox="0 0 24 24"
          >
            <circle
              class="opacity-25"
              cx="12"
              cy="12"
              r="10"
              stroke="currentColor"
              stroke-width="4"
            />
            <path
              class="opacity-75"
              fill="currentColor"
              d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
            />
          </svg>
          {{
            submitting ? t('admin.accounts.bulkEdit.updating') : t('admin.accounts.bulkEdit.submit')
          }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { BulkEditTemplateVersionRecord as BulkEditTemplateVersionRemoteRecord } from '@/api/admin/bulkEditTemplates'
import type { Proxy, AdminGroup, AccountPlatform, AccountType } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import ProxySelector from '@/components/common/ProxySelector.vue'
import GroupSelector from '@/components/common/GroupSelector.vue'
import Icon from '@/components/icons/Icon.vue'
import {
  OPENAI_WS_MODE_DEDICATED,
  OPENAI_WS_MODE_OFF,
  OPENAI_WS_MODE_SHARED
} from '@/utils/openaiWsMode'
import type { OpenAIWSMode } from '@/utils/openaiWsMode'
import { resolveBulkEditScopeCapabilities } from './bulkEditScopeProfile'
import {
  buildBulkEditUpdatePayload,
  hasAnyBulkEditFieldEnabled,
  type BulkEditModelMapping
} from './bulkEditPayload'
import {
  filterBulkEditTemplateRecordsByScope,
  removeBulkEditTemplateRecord,
  normalizeBulkEditTemplateGroupIDs,
  normalizeBulkEditTemplateShareScope,
  upsertBulkEditTemplateRecord,
  type BulkEditTemplateShareScope,
  type BulkEditTemplateRecord
} from './bulkEditTemplateStore'
import {
  createBulkEditTemplateStateSnapshot,
  createDefaultBulkEditTemplateState,
  normalizeBulkEditTemplateState,
  type BulkEditTemplateState
} from './bulkEditTemplateState'
import {
  mapBulkEditTemplateFromRemote,
  mapBulkEditTemplateToUpsertRequest
} from './bulkEditTemplateRemoteMapper'

interface BulkEditTemplateVersionRecord {
  versionID: string
  shareScope: BulkEditTemplateShareScope
  groupIDs: number[]
  state: BulkEditTemplateState
  updatedBy: number
  updatedAt: number
}

interface Props {
  show: boolean
  accountIds: number[]
  scopePlatform?: AccountPlatform | ''
  scopeType?: AccountType | ''
  scopeGroupIds?: number[]
  proxies: Proxy[]
  groups: AdminGroup[]
}

const props = defineProps<Props>()
const emit = defineEmits<{
  close: []
  updated: []
}>()

const { t } = useI18n()
const appStore = useAppStore()

const platformModelPrefix: Record<string, string[]> = {
  anthropic: ['claude-'],
  antigravity: ['claude-', 'gemini-', 'gpt-oss-', 'tab_'],
  openai: ['gpt-'],
  gemini: ['gemini-'],
  sora: []
}

const scopedModelPrefixes = computed(() => {
  if (!props.scopePlatform) return []
  const prefixes = platformModelPrefix[props.scopePlatform] ?? []
  return [...new Set(prefixes)]
})

const filteredModels = computed(() => {
  if (scopedModelPrefixes.value.length === 0) return allModels
  return allModels.filter((model) =>
    scopedModelPrefixes.value.some(prefix => model.value.startsWith(prefix))
  )
})

const filteredPresets = computed(() => {
  if (scopedModelPrefixes.value.length === 0) return presetMappings
  return presetMappings.filter((preset) =>
    scopedModelPrefixes.value.some(prefix => preset.from.startsWith(prefix))
  )
})
// State - field enable flags
const enableBaseUrl = ref(false)
const enableModelRestriction = ref(false)
const enableCustomErrorCodes = ref(false)
const enableInterceptWarmup = ref(false)
const enableOpenAIPassthrough = ref(false)
const enableOpenAIWSMode = ref(false)
const enableCodexCLIOnly = ref(false)
const enableAnthropicPassthrough = ref(false)
const enableProxy = ref(false)
const enableConcurrency = ref(false)
const enablePriority = ref(false)
const enableRateMultiplier = ref(false)
const enableStatus = ref(false)
const enableGroups = ref(false)

// State - field values
const submitting = ref(false)
const baseUrl = ref('')
const modelRestrictionMode = ref<'whitelist' | 'mapping'>('whitelist')
const allowedModels = ref<string[]>([])
const modelMappings = ref<BulkEditModelMapping[]>([])
const selectedErrorCodes = ref<number[]>([])
const customErrorCodeInput = ref<number | null>(null)
const interceptWarmupRequests = ref(false)
const openAIPassthroughEnabled = ref(false)
const openAIWSMode = ref<OpenAIWSMode>(OPENAI_WS_MODE_OFF)
const codexCLIOnlyEnabled = ref(false)
const anthropicPassthroughEnabled = ref(false)
const proxyId = ref<number | null>(null)
const concurrency = ref(1)
const priority = ref(1)
const rateMultiplier = ref(1)
const status = ref<'active' | 'inactive'>('active')
const groupIds = ref<number[]>([])
const templateLoading = ref(false)
const templateName = ref('')
const templateShareScope = ref<BulkEditTemplateShareScope>('private')
const templateShareGroupIds = ref<number[]>([])
const selectedTemplateId = ref<string | null>(null)
const templateRecords = ref<BulkEditTemplateRecord<BulkEditTemplateState>[]>([])
const templateVersionLoading = ref(false)
const templateRollbackingVersionID = ref<string | null>(null)
const templateVersionRecords = ref<BulkEditTemplateVersionRecord[]>([])

// All models list (combined Anthropic + OpenAI + Gemini)
const allModels = [
  { value: 'claude-opus-4-6', label: 'Claude Opus 4.6' },
  { value: 'claude-sonnet-4-6', label: 'Claude Sonnet 4.6' },
  { value: 'claude-opus-4-5-20251101', label: 'Claude Opus 4.5' },
  { value: 'claude-sonnet-4-20250514', label: 'Claude Sonnet 4' },
  { value: 'claude-sonnet-4-5-20250929', label: 'Claude Sonnet 4.5' },
  { value: 'claude-3-5-haiku-20241022', label: 'Claude 3.5 Haiku' },
  { value: 'claude-haiku-4-5-20251001', label: 'Claude Haiku 4.5' },
  { value: 'claude-3-opus-20240229', label: 'Claude 3 Opus' },
  { value: 'claude-3-5-sonnet-20241022', label: 'Claude 3.5 Sonnet' },
  { value: 'claude-3-haiku-20240307', label: 'Claude 3 Haiku' },
  { value: 'gpt-5.3-codex', label: 'GPT-5.3 Codex' },
  { value: 'gpt-5.3-codex-spark', label: 'GPT-5.3 Codex Spark' },
  { value: 'gpt-5.2-2025-12-11', label: 'GPT-5.2' },
  { value: 'gpt-5.2-codex', label: 'GPT-5.2 Codex' },
  { value: 'gpt-5.1-codex-max', label: 'GPT-5.1 Codex Max' },
  { value: 'gpt-5.1-codex', label: 'GPT-5.1 Codex' },
  { value: 'gpt-5.1-2025-11-13', label: 'GPT-5.1' },
  { value: 'gpt-5.1-codex-mini', label: 'GPT-5.1 Codex Mini' },
  { value: 'gpt-5-2025-08-07', label: 'GPT-5' },
  { value: 'gemini-2.0-flash', label: 'Gemini 2.0 Flash' },
  { value: 'gemini-2.5-flash', label: 'Gemini 2.5 Flash' },
  { value: 'gemini-2.5-pro', label: 'Gemini 2.5 Pro' },
  { value: 'gemini-3.1-flash-image', label: 'Gemini 3.1 Flash Image' },
  { value: 'gemini-3-pro-image', label: 'Gemini 3 Pro Image (Legacy)' },
  { value: 'gemini-3-flash-preview', label: 'Gemini 3 Flash Preview' },
  { value: 'gemini-3-pro-preview', label: 'Gemini 3 Pro Preview' }
]

// Preset mappings (combined Anthropic + OpenAI + Gemini)
const presetMappings = [
  {
    label: 'Sonnet 4',
    from: 'claude-sonnet-4-20250514',
    to: 'claude-sonnet-4-20250514',
    color: 'bg-blue-100 text-blue-700 hover:bg-blue-200 dark:bg-blue-900/30 dark:text-blue-400'
  },
  {
    label: 'Sonnet 4.5',
    from: 'claude-sonnet-4-5-20250929',
    to: 'claude-sonnet-4-5-20250929',
    color:
      'bg-indigo-100 text-indigo-700 hover:bg-indigo-200 dark:bg-indigo-900/30 dark:text-indigo-400'
  },
  {
    label: 'Opus 4.5',
    from: 'claude-opus-4-5-20251101',
    to: 'claude-opus-4-5-20251101',
    color:
      'bg-purple-100 text-purple-700 hover:bg-purple-200 dark:bg-purple-900/30 dark:text-purple-400'
  },
  {
    label: 'Opus 4.6',
    from: 'claude-opus-4-6',
    to: 'claude-opus-4-6-thinking',
    color:
      'bg-purple-100 text-purple-700 hover:bg-purple-200 dark:bg-purple-900/30 dark:text-purple-400'
  },
  {
    label: 'Opus 4.6-thinking',
    from: 'claude-opus-4-6-thinking',
    to: 'claude-opus-4-6-thinking',
    color:
      'bg-purple-100 text-purple-700 hover:bg-purple-200 dark:bg-purple-900/30 dark:text-purple-400'
  },
  {
    label: 'Sonnet 4.6',
    from: 'claude-sonnet-4-6',
    to: 'claude-sonnet-4-6',
    color:
      'bg-purple-100 text-purple-700 hover:bg-purple-200 dark:bg-purple-900/30 dark:text-purple-400'
  },
  {
    label: 'Sonnet4→4.6',
    from: 'claude-sonnet-4-20250514',
    to: 'claude-sonnet-4-6',
    color: 'bg-sky-100 text-sky-700 hover:bg-sky-200 dark:bg-sky-900/30 dark:text-sky-400'
  },
  {
    label: 'Sonnet4.5→4.6',
    from: 'claude-sonnet-4-5-20250929',
    to: 'claude-sonnet-4-6',
    color: 'bg-cyan-100 text-cyan-700 hover:bg-cyan-200 dark:bg-cyan-900/30 dark:text-cyan-400'
  },
  {
    label: 'Sonnet3.5→4.6',
    from: 'claude-3-5-sonnet-20241022',
    to: 'claude-sonnet-4-6',
    color: 'bg-teal-100 text-teal-700 hover:bg-teal-200 dark:bg-teal-900/30 dark:text-teal-400'
  },
  {
    label: 'Opus4.5→4.6',
    from: 'claude-opus-4-5-20251101',
    to: 'claude-opus-4-6-thinking',
    color:
      'bg-violet-100 text-violet-700 hover:bg-violet-200 dark:bg-violet-900/30 dark:text-violet-400'
  },
  {
    label: 'Opus->Sonnet',
    from: 'claude-opus-4-5-20251101',
    to: 'claude-sonnet-4-5-20250929',
    color: 'bg-amber-100 text-amber-700 hover:bg-amber-200 dark:bg-amber-900/30 dark:text-amber-400'
  },
  {
    label: 'Gemini 3.1 Image',
    from: 'gemini-3.1-flash-image',
    to: 'gemini-3.1-flash-image',
    color: 'bg-sky-100 text-sky-700 hover:bg-sky-200 dark:bg-sky-900/30 dark:text-sky-400'
  },
  {
    label: 'G3 Image→3.1',
    from: 'gemini-3-pro-image',
    to: 'gemini-3.1-flash-image',
    color: 'bg-sky-100 text-sky-700 hover:bg-sky-200 dark:bg-sky-900/30 dark:text-sky-400'
  },
  {
    label: 'GPT-5.3 Codex',
    from: 'gpt-5.3-codex',
    to: 'gpt-5.3-codex',
    color: 'bg-emerald-100 text-emerald-700 hover:bg-emerald-200 dark:bg-emerald-900/30 dark:text-emerald-400'
  },
  {
    label: 'GPT-5.3 Spark',
    from: 'gpt-5.3-codex-spark',
    to: 'gpt-5.3-codex-spark',
    color: 'bg-emerald-100 text-emerald-700 hover:bg-emerald-200 dark:bg-emerald-900/30 dark:text-emerald-400'
  },
  {
    label: '5.2→5.3',
    from: 'gpt-5.2-codex',
    to: 'gpt-5.3-codex',
    color: 'bg-lime-100 text-lime-700 hover:bg-lime-200 dark:bg-lime-900/30 dark:text-lime-400'
  },
  {
    label: 'GPT-5.2',
    from: 'gpt-5.2-2025-12-11',
    to: 'gpt-5.2-2025-12-11',
    color: 'bg-green-100 text-green-700 hover:bg-green-200 dark:bg-green-900/30 dark:text-green-400'
  },
  {
    label: 'GPT-5.2 Codex',
    from: 'gpt-5.2-codex',
    to: 'gpt-5.2-codex',
    color: 'bg-blue-100 text-blue-700 hover:bg-blue-200 dark:bg-blue-900/30 dark:text-blue-400'
  },
  {
    label: 'Max->Codex',
    from: 'gpt-5.1-codex-max',
    to: 'gpt-5.1-codex',
    color: 'bg-pink-100 text-pink-700 hover:bg-pink-200 dark:bg-pink-900/30 dark:text-pink-400'
  },
  {
    label: '3-Pro-Preview→3.1-Pro-High',
    from: 'gemini-3-pro-preview',
    to: 'gemini-3.1-pro-high',
    color: 'bg-amber-100 text-amber-700 hover:bg-amber-200 dark:bg-amber-900/30 dark:text-amber-400'
  },
  {
    label: '3-Pro-High→3.1-Pro-High',
    from: 'gemini-3-pro-high',
    to: 'gemini-3.1-pro-high',
    color: 'bg-orange-100 text-orange-700 hover:bg-orange-200 dark:bg-orange-900/30 dark:text-orange-400'
  },
  {
    label: '3-Pro-Low→3.1-Pro-Low',
    from: 'gemini-3-pro-low',
    to: 'gemini-3.1-pro-low',
    color: 'bg-yellow-100 text-yellow-700 hover:bg-yellow-200 dark:bg-yellow-900/30 dark:text-yellow-400'
  },
  {
    label: '3-Flash透传',
    from: 'gemini-3-flash',
    to: 'gemini-3-flash',
    color: 'bg-lime-100 text-lime-700 hover:bg-lime-200 dark:bg-lime-900/30 dark:text-lime-400'
  },
  {
    label: '2.5-Flash-Lite透传',
    from: 'gemini-2.5-flash-lite',
    to: 'gemini-2.5-flash-lite',
    color: 'bg-green-100 text-green-700 hover:bg-green-200 dark:bg-green-900/30 dark:text-green-400'
  }
]

// Common HTTP error codes
const commonErrorCodes = [
  { value: 401, label: 'Unauthorized' },
  { value: 403, label: 'Forbidden' },
  { value: 429, label: 'Rate Limit' },
  { value: 500, label: 'Server Error' },
  { value: 502, label: 'Bad Gateway' },
  { value: 503, label: 'Unavailable' },
  { value: 529, label: 'Overloaded' }
]

const statusOptions = computed(() => [
  { value: 'active', label: t('common.active') },
  { value: 'inactive', label: t('common.inactive') }
])

const openAIWSModeOptions = computed(() => [
  { value: OPENAI_WS_MODE_OFF, label: t('admin.accounts.openai.wsModeOff') },
  { value: OPENAI_WS_MODE_SHARED, label: t('admin.accounts.openai.wsModeShared') },
  { value: OPENAI_WS_MODE_DEDICATED, label: t('admin.accounts.openai.wsModeDedicated') }
])

const scopePlatformLabel = computed(() => {
  if (props.scopePlatform === 'anthropic') return t('admin.accounts.platforms.anthropic')
  if (props.scopePlatform === 'openai') return t('admin.accounts.platforms.openai')
  if (props.scopePlatform === 'gemini') return t('admin.accounts.platforms.gemini')
  if (props.scopePlatform === 'antigravity') return t('admin.accounts.platforms.antigravity')
  if (props.scopePlatform === 'sora') return t('admin.accounts.platforms.sora')
  return '-'
})

const scopeTypeLabel = computed(() => {
  if (props.scopeType === 'oauth') return t('admin.accounts.oauthType')
  if (props.scopeType === 'setup-token') return t('admin.accounts.setupToken')
  if (props.scopeType === 'apikey') return t('admin.accounts.apiKey')
  if (props.scopeType === 'upstream') return t('admin.accounts.types.upstream')
  return '-'
})

const scopedDialogTitle = computed(() => {
  if (!props.scopePlatform || !props.scopeType) return t('admin.accounts.bulkEdit.title')
  return `${t('admin.accounts.bulkEdit.title')} · ${scopePlatformLabel.value} / ${scopeTypeLabel.value}`
})

const scopeCapabilities = computed(() =>
  resolveBulkEditScopeCapabilities(props.scopePlatform, props.scopeType)
)

const supportsBaseUrl = computed(() => scopeCapabilities.value.supportsBaseUrl)
const supportsModelRestriction = computed(() => scopeCapabilities.value.supportsModelRestriction)
const supportsCustomErrorCodes = computed(() => scopeCapabilities.value.supportsCustomErrorCodes)
const supportsInterceptWarmup = computed(() => scopeCapabilities.value.supportsInterceptWarmup)
const supportsOpenAIPassthrough = computed(() => scopeCapabilities.value.supportsOpenAIPassthrough)
const supportsOpenAIWSMode = computed(() => scopeCapabilities.value.supportsOpenAIWSMode)
const supportsCodexCLIOnly = computed(() => scopeCapabilities.value.supportsCodexCLIOnly)
const supportsAnthropicPassthrough = computed(() => scopeCapabilities.value.supportsAnthropicPassthrough)
const hasTemplateScope = computed(() => Boolean(props.scopePlatform && props.scopeType))
const normalizedScopeGroupIDs = computed(() =>
  normalizeBulkEditTemplateGroupIDs(props.scopeGroupIds ?? [])
)

const scopedTemplateRecords = computed(() =>
  filterBulkEditTemplateRecordsByScope(
    templateRecords.value,
    props.scopePlatform ?? '',
    props.scopeType ?? ''
  )
)

const templateOptions = computed(() =>
  scopedTemplateRecords.value.map((template) => ({
    value: template.id,
    label: template.name
  }))
)

const selectedTemplateRecord = computed(() =>
  scopedTemplateRecords.value.find((template) => template.id === selectedTemplateId.value) ?? null
)

const canApplySelectedTemplate = computed(() => Boolean(selectedTemplateRecord.value))
const canDeleteSelectedTemplate = computed(() => Boolean(selectedTemplateRecord.value))
const templateShareScopeOptions = computed(() => [
  {
    value: 'private',
    label: t('admin.accounts.bulkEdit.templateShareScopePrivate')
  },
  {
    value: 'team',
    label: t('admin.accounts.bulkEdit.templateShareScopeTeam')
  },
  {
    value: 'groups',
    label: t('admin.accounts.bulkEdit.templateShareScopeGroups')
  }
])
const canSaveTemplate = computed(() => {
  if (!hasTemplateScope.value || templateName.value.trim().length === 0) return false
  if (templateShareScope.value === 'groups' && templateShareGroupIds.value.length === 0) {
    return false
  }
  return true
})

const resolveTemplateShareScopeLabel = (scope: BulkEditTemplateShareScope): string => {
  if (scope === 'team') return t('admin.accounts.bulkEdit.templateShareScopeTeam')
  if (scope === 'groups') return t('admin.accounts.bulkEdit.templateShareScopeGroups')
  return t('admin.accounts.bulkEdit.templateShareScopePrivate')
}

const formatTemplateVersionUpdatedAt = (value: number): string => {
  if (!Number.isFinite(value) || value <= 0) {
    return t('admin.accounts.bulkEdit.templateVersionUnknownTime')
  }
  return new Date(value).toLocaleString()
}

const mapTemplateVersionFromRemote = (
  record: BulkEditTemplateVersionRemoteRecord<BulkEditTemplateState>
): BulkEditTemplateVersionRecord => ({
  versionID: typeof record.version_id === 'string' ? record.version_id : '',
  shareScope: normalizeBulkEditTemplateShareScope(record.share_scope),
  groupIDs: normalizeBulkEditTemplateGroupIDs(record.group_ids),
  state: normalizeBulkEditTemplateState(record.state),
  updatedBy:
    typeof record.updated_by === 'number' && Number.isFinite(record.updated_by)
      ? Math.floor(record.updated_by)
      : 0,
  updatedAt:
    typeof record.updated_at === 'number' && Number.isFinite(record.updated_at) && record.updated_at > 0
      ? Math.floor(record.updated_at)
      : Date.now()
})

const collectTemplateState = (): BulkEditTemplateState => {
  return createBulkEditTemplateStateSnapshot({
    enableBaseUrl: enableBaseUrl.value,
    enableModelRestriction: enableModelRestriction.value,
    enableCustomErrorCodes: enableCustomErrorCodes.value,
    enableInterceptWarmup: enableInterceptWarmup.value,
    enableOpenAIPassthrough: enableOpenAIPassthrough.value,
    enableOpenAIWSMode: enableOpenAIWSMode.value,
    enableCodexCLIOnly: enableCodexCLIOnly.value,
    enableAnthropicPassthrough: enableAnthropicPassthrough.value,
    enableProxy: enableProxy.value,
    enableConcurrency: enableConcurrency.value,
    enablePriority: enablePriority.value,
    enableRateMultiplier: enableRateMultiplier.value,
    enableStatus: enableStatus.value,
    enableGroups: enableGroups.value,
    baseUrl: baseUrl.value,
    modelRestrictionMode: modelRestrictionMode.value,
    allowedModels: allowedModels.value,
    modelMappings: modelMappings.value,
    selectedErrorCodes: selectedErrorCodes.value,
    interceptWarmupRequests: interceptWarmupRequests.value,
    openAIPassthroughEnabled: openAIPassthroughEnabled.value,
    openAIWSMode: openAIWSMode.value,
    codexCLIOnlyEnabled: codexCLIOnlyEnabled.value,
    anthropicPassthroughEnabled: anthropicPassthroughEnabled.value,
    proxyId: proxyId.value,
    concurrency: concurrency.value,
    priority: priority.value,
    rateMultiplier: rateMultiplier.value,
    status: status.value,
    groupIds: groupIds.value
  })
}

const applyTemplateState = (value: unknown) => {
  const normalized = normalizeBulkEditTemplateState(value)

  enableBaseUrl.value = normalized.enableBaseUrl
  enableModelRestriction.value = normalized.enableModelRestriction
  enableCustomErrorCodes.value = normalized.enableCustomErrorCodes
  enableInterceptWarmup.value = normalized.enableInterceptWarmup
  enableOpenAIPassthrough.value = normalized.enableOpenAIPassthrough
  enableOpenAIWSMode.value = normalized.enableOpenAIWSMode
  enableCodexCLIOnly.value = normalized.enableCodexCLIOnly
  enableAnthropicPassthrough.value = normalized.enableAnthropicPassthrough
  enableProxy.value = normalized.enableProxy
  enableConcurrency.value = normalized.enableConcurrency
  enablePriority.value = normalized.enablePriority
  enableRateMultiplier.value = normalized.enableRateMultiplier
  enableStatus.value = normalized.enableStatus
  enableGroups.value = normalized.enableGroups

  baseUrl.value = normalized.baseUrl
  modelRestrictionMode.value = normalized.modelRestrictionMode
  allowedModels.value = [...normalized.allowedModels]
  modelMappings.value = normalized.modelMappings.map((item) => ({
    from: item.from,
    to: item.to
  }))
  selectedErrorCodes.value = [...normalized.selectedErrorCodes]
  customErrorCodeInput.value = null
  interceptWarmupRequests.value = normalized.interceptWarmupRequests
  openAIPassthroughEnabled.value = normalized.openAIPassthroughEnabled
  openAIWSMode.value = normalized.openAIWSMode
  codexCLIOnlyEnabled.value = normalized.codexCLIOnlyEnabled
  anthropicPassthroughEnabled.value = normalized.anthropicPassthroughEnabled
  proxyId.value = normalized.proxyId
  concurrency.value = normalized.concurrency
  priority.value = normalized.priority
  rateMultiplier.value = normalized.rateMultiplier
  status.value = normalized.status
  groupIds.value = [...normalized.groupIds]
}

const resetFormState = () => {
  applyTemplateState(createDefaultBulkEditTemplateState())
  templateName.value = ''
  templateShareScope.value = 'private'
  templateShareGroupIds.value = []
  selectedTemplateId.value = null
  templateVersionRecords.value = []
  templateRollbackingVersionID.value = null
  templateVersionLoading.value = false
}

const syncSelectedTemplate = () => {
  if (scopedTemplateRecords.value.length === 0) {
    selectedTemplateId.value = null
    return
  }
  if (
    !selectedTemplateId.value ||
    !scopedTemplateRecords.value.some((item) => item.id === selectedTemplateId.value)
  ) {
    selectedTemplateId.value = scopedTemplateRecords.value[0].id
  }
}

const loadTemplateRecordsFromServer = async () => {
  if (!props.scopePlatform || !props.scopeType) {
    templateRecords.value = []
    templateVersionRecords.value = []
    return
  }

  templateLoading.value = true
  try {
    const remoteRecords = await adminAPI.bulkEditTemplates.getBulkEditTemplates<BulkEditTemplateState>({
      scope_platform: props.scopePlatform,
      scope_type: props.scopeType,
      scope_group_ids: normalizedScopeGroupIDs.value
    })

    templateRecords.value = remoteRecords.map((record) => {
      const mapped = mapBulkEditTemplateFromRemote<BulkEditTemplateState>(record)
      return {
        ...mapped,
        state: normalizeBulkEditTemplateState(mapped.state)
      }
    })
  } catch (error) {
    templateRecords.value = []
    appStore.showError(t('admin.accounts.bulkEdit.templateLoadFailed'))
    console.error('Failed to load bulk edit templates:', error)
  } finally {
    templateLoading.value = false
  }
}

const loadTemplateVersionRecordsFromServer = async () => {
  const selected = selectedTemplateRecord.value
  if (!selected) {
    templateVersionRecords.value = []
    return
  }

  templateVersionLoading.value = true
  try {
    const remoteRecords = await adminAPI.bulkEditTemplates.getBulkEditTemplateVersions<BulkEditTemplateState>(
      selected.id,
      {
        scope_group_ids: normalizedScopeGroupIDs.value
      }
    )
    templateVersionRecords.value = remoteRecords
      .map(mapTemplateVersionFromRemote)
      .filter((item) => item.versionID)
      .sort((a, b) => b.updatedAt - a.updatedAt)
  } catch (error) {
    templateVersionRecords.value = []
    appStore.showError(t('admin.accounts.bulkEdit.templateVersionLoadFailed'))
    console.error('Failed to load bulk edit template versions:', error)
  } finally {
    templateVersionLoading.value = false
  }
}

const saveTemplate = async () => {
  if (!props.scopePlatform || !props.scopeType) return
  const name = templateName.value.trim()
  if (!name) {
    appStore.showError(t('admin.accounts.bulkEdit.templateNameRequired'))
    return
  }
  if (templateShareScope.value === 'groups' && templateShareGroupIds.value.length === 0) {
    appStore.showError(t('admin.accounts.bulkEdit.templateShareGroupsRequired'))
    return
  }

  const matchedByName = scopedTemplateRecords.value.find(
    (item) => item.name.trim().toLowerCase() === name.toLowerCase()
  )
  const request = mapBulkEditTemplateToUpsertRequest({
    id: matchedByName?.id,
    name,
    scopePlatform: props.scopePlatform,
    scopeType: props.scopeType,
    shareScope: normalizeBulkEditTemplateShareScope(templateShareScope.value),
    groupIds: normalizeBulkEditTemplateGroupIDs(templateShareGroupIds.value),
    state: collectTemplateState()
  })

  try {
    const savedRemote = await adminAPI.bulkEditTemplates.upsertBulkEditTemplate<BulkEditTemplateState>(request)
    const savedRecord = mapBulkEditTemplateFromRemote(savedRemote)
    const normalizedRecord: BulkEditTemplateRecord<BulkEditTemplateState> = {
      ...savedRecord,
      state: normalizeBulkEditTemplateState(savedRecord.state)
    }
    templateRecords.value = upsertBulkEditTemplateRecord(templateRecords.value, normalizedRecord)
    selectedTemplateId.value = normalizedRecord.id
    templateName.value = normalizedRecord.name
    templateShareScope.value = normalizedRecord.shareScope
    templateShareGroupIds.value = [...normalizedRecord.groupIds]
    await loadTemplateVersionRecordsFromServer()
    appStore.showSuccess(t('admin.accounts.bulkEdit.templateSaved', { name: normalizedRecord.name }))
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.accounts.bulkEdit.templateSaveFailed'))
    console.error('Failed to save bulk edit template:', error)
  }
}

const applySelectedTemplate = () => {
  if (!selectedTemplateRecord.value) return
  applyTemplateState(selectedTemplateRecord.value.state)
  templateName.value = selectedTemplateRecord.value.name
  templateShareScope.value = selectedTemplateRecord.value.shareScope
  templateShareGroupIds.value = [...selectedTemplateRecord.value.groupIds]
  appStore.showInfo(
    t('admin.accounts.bulkEdit.templateApplied', { name: selectedTemplateRecord.value.name })
  )
}

const removeSelectedTemplate = async () => {
  const template = selectedTemplateRecord.value
  if (!template) return

  const confirmed = confirm(t('admin.accounts.bulkEdit.templateDeleteConfirm', { name: template.name }))
  if (!confirmed) return

  try {
    await adminAPI.bulkEditTemplates.deleteBulkEditTemplate(template.id)
    templateRecords.value = removeBulkEditTemplateRecord(templateRecords.value, template.id)
    selectedTemplateId.value = null
    templateName.value = ''
    templateShareScope.value = 'private'
    templateShareGroupIds.value = []
    templateVersionRecords.value = []
    templateRollbackingVersionID.value = null
    syncSelectedTemplate()
    appStore.showSuccess(t('admin.accounts.bulkEdit.templateDeleted', { name: template.name }))
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.accounts.bulkEdit.templateDeleteFailed'))
    console.error('Failed to delete bulk edit template:', error)
  }
}

const rollbackTemplateVersion = async (versionID: string) => {
  const template = selectedTemplateRecord.value
  if (!template || !versionID) return

  const targetVersion = templateVersionRecords.value.find((item) => item.versionID === versionID)
  if (!targetVersion) return

  const confirmed = confirm(
    t('admin.accounts.bulkEdit.templateRollbackConfirm', {
      name: template.name,
      updatedAt: formatTemplateVersionUpdatedAt(targetVersion.updatedAt)
    })
  )
  if (!confirmed) return

  templateRollbackingVersionID.value = versionID
  try {
    const rollbackedRemote = await adminAPI.bulkEditTemplates.rollbackBulkEditTemplate<BulkEditTemplateState>(
      template.id,
      { version_id: versionID },
      { scope_group_ids: normalizedScopeGroupIDs.value }
    )
    const rollbackedMapped = mapBulkEditTemplateFromRemote<BulkEditTemplateState>(rollbackedRemote)
    const rollbackedRecord: BulkEditTemplateRecord<BulkEditTemplateState> = {
      ...rollbackedMapped,
      state: normalizeBulkEditTemplateState(rollbackedMapped.state)
    }

    templateRecords.value = upsertBulkEditTemplateRecord(templateRecords.value, rollbackedRecord)
    selectedTemplateId.value = rollbackedRecord.id
    applyTemplateState(rollbackedRecord.state)
    templateName.value = rollbackedRecord.name
    templateShareScope.value = rollbackedRecord.shareScope
    templateShareGroupIds.value = [...rollbackedRecord.groupIds]

    await loadTemplateVersionRecordsFromServer()
    appStore.showSuccess(t('admin.accounts.bulkEdit.templateRollbackSuccess', { name: template.name }))
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.accounts.bulkEdit.templateRollbackFailed'))
    console.error('Failed to rollback bulk edit template:', error)
  } finally {
    templateRollbackingVersionID.value = null
  }
}

// Model mapping helpers
const addModelMapping = () => {
  modelMappings.value.push({ from: '', to: '' })
}

const removeModelMapping = (index: number) => {
  modelMappings.value.splice(index, 1)
}

const addPresetMapping = (from: string, to: string) => {
  const exists = modelMappings.value.some((m) => m.from === from)
  if (exists) {
    appStore.showInfo(t('admin.accounts.mappingExists', { model: from }))
    return
  }
  modelMappings.value.push({ from, to })
}

// Error code helpers
const toggleErrorCode = (code: number) => {
  const index = selectedErrorCodes.value.indexOf(code)
  if (index === -1) {
    // Adding code - check for 429/529 warning
    if (code === 429) {
      if (!confirm(t('admin.accounts.customErrorCodes429Warning'))) {
        return
      }
    } else if (code === 529) {
      if (!confirm(t('admin.accounts.customErrorCodes529Warning'))) {
        return
      }
    }
    selectedErrorCodes.value.push(code)
  } else {
    selectedErrorCodes.value.splice(index, 1)
  }
}

const addCustomErrorCode = () => {
  const code = customErrorCodeInput.value
  if (code === null || code < 100 || code > 599) {
    appStore.showError(t('admin.accounts.invalidErrorCode'))
    return
  }
  if (selectedErrorCodes.value.includes(code)) {
    appStore.showInfo(t('admin.accounts.errorCodeExists'))
    return
  }
  // Check for 429/529 warning
  if (code === 429) {
    if (!confirm(t('admin.accounts.customErrorCodes429Warning'))) {
      return
    }
  } else if (code === 529) {
    if (!confirm(t('admin.accounts.customErrorCodes529Warning'))) {
      return
    }
  }
  selectedErrorCodes.value.push(code)
  customErrorCodeInput.value = null
}

const removeErrorCode = (code: number) => {
  const index = selectedErrorCodes.value.indexOf(code)
  if (index !== -1) {
    selectedErrorCodes.value.splice(index, 1)
  }
}

const buildUpdatePayload = (): Record<string, unknown> | null => {
  return buildBulkEditUpdatePayload({
    scopeType: props.scopeType,
    enableBaseUrl: enableBaseUrl.value,
    enableModelRestriction: enableModelRestriction.value,
    enableCustomErrorCodes: enableCustomErrorCodes.value,
    enableInterceptWarmup: enableInterceptWarmup.value,
    enableOpenAIPassthrough: enableOpenAIPassthrough.value,
    enableOpenAIWSMode: enableOpenAIWSMode.value,
    enableCodexCLIOnly: enableCodexCLIOnly.value,
    enableAnthropicPassthrough: enableAnthropicPassthrough.value,
    enableProxy: enableProxy.value,
    enableConcurrency: enableConcurrency.value,
    enablePriority: enablePriority.value,
    enableRateMultiplier: enableRateMultiplier.value,
    enableStatus: enableStatus.value,
    enableGroups: enableGroups.value,
    baseUrl: baseUrl.value,
    modelRestrictionMode: modelRestrictionMode.value,
    allowedModels: allowedModels.value,
    modelMappings: modelMappings.value,
    selectedErrorCodes: selectedErrorCodes.value,
    interceptWarmupRequests: interceptWarmupRequests.value,
    openAIPassthroughEnabled: openAIPassthroughEnabled.value,
    openAIWSMode: openAIWSMode.value,
    codexCLIOnlyEnabled: codexCLIOnlyEnabled.value,
    anthropicPassthroughEnabled: anthropicPassthroughEnabled.value,
    proxyId: proxyId.value,
    concurrency: concurrency.value,
    priority: priority.value,
    rateMultiplier: rateMultiplier.value,
    status: status.value,
    groupIds: groupIds.value
  })
}

const handleClose = () => {
  emit('close')
}

const handleSubmit = async () => {
  if (props.accountIds.length === 0) {
    appStore.showError(t('admin.accounts.bulkEdit.noSelection'))
    return
  }

  const hasAnyFieldEnabled = hasAnyBulkEditFieldEnabled({
    enableBaseUrl: enableBaseUrl.value,
    enableModelRestriction: enableModelRestriction.value,
    enableCustomErrorCodes: enableCustomErrorCodes.value,
    enableInterceptWarmup: enableInterceptWarmup.value,
    enableOpenAIPassthrough: enableOpenAIPassthrough.value,
    enableOpenAIWSMode: enableOpenAIWSMode.value,
    enableCodexCLIOnly: enableCodexCLIOnly.value,
    enableAnthropicPassthrough: enableAnthropicPassthrough.value,
    enableProxy: enableProxy.value,
    enableConcurrency: enableConcurrency.value,
    enablePriority: enablePriority.value,
    enableRateMultiplier: enableRateMultiplier.value,
    enableStatus: enableStatus.value,
    enableGroups: enableGroups.value
  })

  if (!hasAnyFieldEnabled) {
    appStore.showError(t('admin.accounts.bulkEdit.noFieldsSelected'))
    return
  }

  const updates = buildUpdatePayload()
  if (!updates) {
    appStore.showError(t('admin.accounts.bulkEdit.noFieldsSelected'))
    return
  }

  submitting.value = true

  try {
    const res = await adminAPI.accounts.bulkUpdate(props.accountIds, updates)
    const success = res.success || 0
    const failed = res.failed || 0

    if (success > 0 && failed === 0) {
      appStore.showSuccess(t('admin.accounts.bulkEdit.success', { count: success }))
    } else if (success > 0) {
      appStore.showError(t('admin.accounts.bulkEdit.partialSuccess', { success, failed }))
    } else {
      appStore.showError(t('admin.accounts.bulkEdit.failed'))
    }

    if (success > 0) {
      emit('updated')
      handleClose()
    }
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.accounts.bulkEdit.failed'))
    console.error('Error bulk updating accounts:', error)
  } finally {
    submitting.value = false
  }
}

watch(
  scopedTemplateRecords,
  () => {
    syncSelectedTemplate()
  },
  { immediate: true }
)

watch(selectedTemplateId, (id) => {
  if (!id) {
    templateVersionRecords.value = []
    return
  }
  const selected = scopedTemplateRecords.value.find((item) => item.id === id)
  if (selected) {
    templateName.value = selected.name
    templateShareScope.value = selected.shareScope
    templateShareGroupIds.value = [...selected.groupIds]
    void loadTemplateVersionRecordsFromServer()
  }
})

watch(templateShareScope, (scope) => {
  if (scope !== 'groups') {
    templateShareGroupIds.value = []
  }
})

watch(
  () => normalizedScopeGroupIDs.value.join(','),
  () => {
    if (!props.show) return
    void loadTemplateRecordsFromServer()
  }
)

// Reset form when modal closes
watch(
  () => props.show,
  (newShow) => {
    if (newShow) {
      void loadTemplateRecordsFromServer()
      return
    }
    resetFormState()
  }
)
</script>
