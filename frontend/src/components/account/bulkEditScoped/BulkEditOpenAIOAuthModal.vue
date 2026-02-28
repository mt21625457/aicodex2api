<template>
  <BulkEditAccountModal
    :show="show"
    :account-ids="accountIds"
    :scope-group-ids="scopeGroupIds"
    scope-platform="openai"
    scope-type="oauth"
    :proxies="proxies"
    :groups="groups"
    @close="emit('close')"
    @updated="emit('updated')"
  />
</template>

<script setup lang="ts">
import type { Proxy, AdminGroup } from '@/types'
import BulkEditAccountModal from '../BulkEditAccountModal.vue'

interface Props {
  show: boolean
  accountIds: number[]
  scopeGroupIds?: number[]
  proxies: Proxy[]
  groups: AdminGroup[]
}

defineProps<Props>()
const emit = defineEmits<{
  close: []
  updated: []
}>()
</script>
