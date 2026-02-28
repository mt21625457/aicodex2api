<template>
  <BulkEditAccountModal
    v-if="useFallbackComponent"
    :show="show"
    :account-ids="accountIds"
    :scope-platform="scopePlatform"
    :scope-type="scopeType"
    :scope-group-ids="scopeGroupIds"
    :proxies="proxies"
    :groups="groups"
    @close="emit('close')"
    @updated="emit('updated')"
  />
  <component
    v-else
    :is="resolvedScopeComponent"
    :show="show"
    :account-ids="accountIds"
    :scope-group-ids="scopeGroupIds"
    :proxies="proxies"
    :groups="groups"
    @close="emit('close')"
    @updated="emit('updated')"
  />
</template>

<script setup lang="ts">
import { computed, type Component } from 'vue'
import type { AccountPlatform, AccountType, AdminGroup, Proxy } from '@/types'
import BulkEditAccountModal from './BulkEditAccountModal.vue'
import BulkEditAnthropicOAuthModal from './bulkEditScoped/BulkEditAnthropicOAuthModal.vue'
import BulkEditAnthropicSetupTokenModal from './bulkEditScoped/BulkEditAnthropicSetupTokenModal.vue'
import BulkEditAnthropicApiKeyModal from './bulkEditScoped/BulkEditAnthropicApiKeyModal.vue'
import BulkEditOpenAIOAuthModal from './bulkEditScoped/BulkEditOpenAIOAuthModal.vue'
import BulkEditOpenAIApiKeyModal from './bulkEditScoped/BulkEditOpenAIApiKeyModal.vue'
import BulkEditGeminiOAuthModal from './bulkEditScoped/BulkEditGeminiOAuthModal.vue'
import BulkEditGeminiApiKeyModal from './bulkEditScoped/BulkEditGeminiApiKeyModal.vue'
import BulkEditAntigravityOAuthModal from './bulkEditScoped/BulkEditAntigravityOAuthModal.vue'
import BulkEditAntigravityUpstreamModal from './bulkEditScoped/BulkEditAntigravityUpstreamModal.vue'
import BulkEditSoraOAuthModal from './bulkEditScoped/BulkEditSoraOAuthModal.vue'
import BulkEditSoraApiKeyModal from './bulkEditScoped/BulkEditSoraApiKeyModal.vue'
import {
  resolveBulkEditScopeEditorKey,
  type BulkEditScopeEditorKey
} from './bulkEditScopeProfile'

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

const scopeComponentMap: Record<BulkEditScopeEditorKey, Component> = {
  'anthropic:oauth': BulkEditAnthropicOAuthModal,
  'anthropic:setup-token': BulkEditAnthropicSetupTokenModal,
  'anthropic:apikey': BulkEditAnthropicApiKeyModal,
  'openai:oauth': BulkEditOpenAIOAuthModal,
  'openai:apikey': BulkEditOpenAIApiKeyModal,
  'gemini:oauth': BulkEditGeminiOAuthModal,
  'gemini:apikey': BulkEditGeminiApiKeyModal,
  'antigravity:oauth': BulkEditAntigravityOAuthModal,
  'antigravity:upstream': BulkEditAntigravityUpstreamModal,
  'sora:oauth': BulkEditSoraOAuthModal,
  'sora:apikey': BulkEditSoraApiKeyModal
}

const scopeEditorKey = computed(() =>
  resolveBulkEditScopeEditorKey(props.scopePlatform, props.scopeType)
)
const resolvedScopeComponent = computed(() =>
  scopeEditorKey.value ? scopeComponentMap[scopeEditorKey.value] : null
)
const useFallbackComponent = computed(() => !resolvedScopeComponent.value)
</script>
