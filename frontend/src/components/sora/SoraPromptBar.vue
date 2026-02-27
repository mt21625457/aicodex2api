<template>
  <div class="sticky bottom-0 border-t border-gray-200 bg-white px-4 py-3 dark:border-dark-700 dark:bg-dark-900">
    <!-- 参考图缩略图预览 -->
    <div v-if="imagePreview" class="mb-2 flex items-center gap-2">
      <div class="relative inline-block">
        <img :src="imagePreview" :alt="t('sora.referenceImage')" class="h-16 w-16 rounded-lg border border-gray-200 object-cover dark:border-dark-600" />
        <button
          class="absolute -right-1.5 -top-1.5 flex h-5 w-5 items-center justify-center rounded-full bg-red-500 text-white shadow hover:bg-red-600"
          :title="t('sora.removeImage')"
          @click="removeImage"
        >
          <svg class="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" /></svg>
        </button>
      </div>
      <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('sora.referenceImage') }}</span>
    </div>

    <div class="flex items-end gap-3">
      <!-- 模型选择 -->
      <div class="shrink-0">
        <div class="flex items-center gap-2">
          <select
            v-model="selectedModel"
            class="rounded-lg border border-gray-300 bg-white px-3 py-2.5 text-sm focus:border-primary-500 focus:outline-none focus:ring-1 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200"
          >
            <option v-for="m in models" :key="m.id" :value="m.id">{{ m.name }}</option>
          </select>
          <span
            v-if="selectedModelType"
            class="inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium"
            :class="selectedModelType === 'video'
              ? 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400'
              : 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-400'"
          >
            {{ selectedModelType === 'video' ? t('sora.mediaTypeVideo') : t('sora.mediaTypeImage') }}
          </span>
        </div>
      </div>

      <!-- 参考图上传按钮 -->
      <button
        class="shrink-0 rounded-lg border border-gray-300 p-2.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:border-dark-600 dark:text-gray-400 dark:hover:bg-dark-700 dark:hover:text-gray-200"
        :title="t('sora.referenceImage')"
        @click="triggerFileInput"
      >
        <svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16l4.586-4.586a2 2 0 012.828 0L16 16m-2-2l1.586-1.586a2 2 0 012.828 0L20 14m-6-6h.01M6 20h12a2 2 0 002-2V6a2 2 0 00-2-2H6a2 2 0 00-2 2v12a2 2 0 002 2z" />
        </svg>
      </button>
      <input
        ref="fileInputRef"
        type="file"
        accept="image/png,image/jpeg,image/webp"
        class="hidden"
        @change="onFileChange"
      />

      <!-- 提示词输入 -->
      <div class="flex-1">
        <textarea
          v-model="prompt"
          :placeholder="t('sora.promptPlaceholder')"
          rows="2"
          class="w-full resize-none rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm focus:border-primary-500 focus:outline-none focus:ring-1 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200 dark:placeholder-gray-500"
          @keydown.enter.ctrl="submit"
          @keydown.enter.meta="submit"
        />
      </div>

      <!-- 生成按钮 -->
      <button
        :disabled="!canSubmit || generating"
        class="shrink-0 rounded-lg bg-primary-600 px-5 py-2.5 text-sm font-medium text-white transition-colors hover:bg-primary-700 disabled:cursor-not-allowed disabled:opacity-50"
        @click="submit"
      >
        {{ generating ? t('sora.generating') : t('sora.generate') }}
      </button>
    </div>

    <!-- 文件大小错误提示 -->
    <p v-if="imageError" class="mt-1 text-xs text-red-500">{{ imageError }}</p>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import soraAPI, { type SoraModel, type GenerateRequest } from '@/api/sora'

const MAX_IMAGE_SIZE = 20 * 1024 * 1024 // 20MB

const props = defineProps<{ generating: boolean }>()
const emit = defineEmits<{ generate: [req: GenerateRequest] }>()
const { t } = useI18n()

const prompt = ref('')
const selectedModel = ref('')
const models = ref<SoraModel[]>([])
const imagePreview = ref<string | null>(null)
const imageError = ref('')
const fileInputRef = ref<HTMLInputElement | null>(null)

const canSubmit = computed(() => prompt.value.trim().length > 0 && selectedModel.value)

/** 当前选中模型的媒体类型（video / image） */
const selectedModelType = computed(() => {
  const model = models.value.find(m => m.id === selectedModel.value)
  return model?.type || null
})

async function loadModels() {
  try {
    models.value = await soraAPI.getModels()
    if (models.value.length > 0 && !selectedModel.value) {
      selectedModel.value = models.value[0].id
    }
  } catch (e) {
    console.error('Failed to load models:', e)
  }
}

function triggerFileInput() {
  fileInputRef.value?.click()
}

function onFileChange(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return

  // 重置错误
  imageError.value = ''

  // 文件大小校验
  if (file.size > MAX_IMAGE_SIZE) {
    imageError.value = t('sora.imageTooLarge')
    input.value = ''
    return
  }

  const reader = new FileReader()
  reader.onload = (e) => {
    imagePreview.value = e.target?.result as string
  }
  reader.readAsDataURL(file)

  // 重置 input 以便重复选择同一文件
  input.value = ''
}

function removeImage() {
  imagePreview.value = null
  imageError.value = ''
}

function submit() {
  if (!canSubmit.value || props.generating) return
  const req: GenerateRequest = {
    model: selectedModel.value,
    prompt: prompt.value.trim(),
    media_type: models.value.find(m => m.id === selectedModel.value)?.type || 'video'
  }
  if (imagePreview.value) {
    req.image_input = imagePreview.value
  }
  emit('generate', req)
  prompt.value = ''
  imagePreview.value = null
  imageError.value = ''
}

onMounted(loadModels)
</script>
