import type { AccountType } from '@/types'
import { buildModelMappingObject as buildModelMappingPayload } from '@/composables/useModelWhitelist'
import { isOpenAIWSModeEnabled, type OpenAIWSMode } from '@/utils/openaiWsMode'

export interface BulkEditModelMapping {
  from: string
  to: string
}

export interface BulkEditPayloadInput {
  scopeType?: AccountType | '' | null
  enableBaseUrl: boolean
  enableModelRestriction: boolean
  enableCustomErrorCodes: boolean
  enableInterceptWarmup: boolean
  enableOpenAIPassthrough: boolean
  enableOpenAIWSMode: boolean
  enableCodexCLIOnly: boolean
  enableAnthropicPassthrough: boolean
  enableProxy: boolean
  enableConcurrency: boolean
  enablePriority: boolean
  enableRateMultiplier: boolean
  enableStatus: boolean
  enableGroups: boolean
  baseUrl: string
  modelRestrictionMode: 'whitelist' | 'mapping'
  allowedModels: string[]
  modelMappings: BulkEditModelMapping[]
  selectedErrorCodes: number[]
  interceptWarmupRequests: boolean
  openAIPassthroughEnabled: boolean
  openAIWSMode: OpenAIWSMode
  codexCLIOnlyEnabled: boolean
  anthropicPassthroughEnabled: boolean
  proxyId: number | null
  concurrency: number
  priority: number
  rateMultiplier: number
  status: 'active' | 'inactive'
  groupIds: number[]
}

type BulkEditEnabledFlags = Pick<
  BulkEditPayloadInput,
  | 'enableBaseUrl'
  | 'enableModelRestriction'
  | 'enableCustomErrorCodes'
  | 'enableInterceptWarmup'
  | 'enableOpenAIPassthrough'
  | 'enableOpenAIWSMode'
  | 'enableCodexCLIOnly'
  | 'enableAnthropicPassthrough'
  | 'enableProxy'
  | 'enableConcurrency'
  | 'enablePriority'
  | 'enableRateMultiplier'
  | 'enableStatus'
  | 'enableGroups'
>

export const hasAnyBulkEditFieldEnabled = (flags: BulkEditEnabledFlags): boolean => {
  return (
    flags.enableBaseUrl ||
    flags.enableModelRestriction ||
    flags.enableCustomErrorCodes ||
    flags.enableInterceptWarmup ||
    flags.enableOpenAIPassthrough ||
    flags.enableOpenAIWSMode ||
    flags.enableCodexCLIOnly ||
    flags.enableAnthropicPassthrough ||
    flags.enableProxy ||
    flags.enableConcurrency ||
    flags.enablePriority ||
    flags.enableRateMultiplier ||
    flags.enableStatus ||
    flags.enableGroups
  )
}

export const buildBulkEditUpdatePayload = (
  input: BulkEditPayloadInput
): Record<string, unknown> | null => {
  const updates: Record<string, unknown> = {}
  const credentials: Record<string, unknown> = {}
  const extra: Record<string, unknown> = {}
  let credentialsChanged = false
  let extraChanged = false

  if (input.enableProxy) {
    // Backend expects `proxy_id: 0` to clear proxy.
    updates.proxy_id = input.proxyId === null ? 0 : input.proxyId
  }

  if (input.enableConcurrency) {
    updates.concurrency = input.concurrency
  }

  if (input.enablePriority) {
    updates.priority = input.priority
  }

  if (input.enableRateMultiplier) {
    updates.rate_multiplier = input.rateMultiplier
  }

  if (input.enableStatus) {
    updates.status = input.status
  }

  if (input.enableGroups) {
    updates.group_ids = input.groupIds
  }

  if (input.enableBaseUrl) {
    const baseUrlValue = input.baseUrl.trim()
    if (baseUrlValue) {
      credentials.base_url = baseUrlValue
      credentialsChanged = true
    }
  }

  if (input.enableModelRestriction) {
    if (input.modelRestrictionMode === 'whitelist') {
      if (input.allowedModels.length > 0) {
        const mapping: Record<string, string> = {}
        for (const model of input.allowedModels) {
          mapping[model] = model
        }
        credentials.model_mapping = mapping
        credentialsChanged = true
      }
    } else {
      const modelMapping = buildModelMappingPayload(
        input.modelRestrictionMode,
        input.allowedModels,
        input.modelMappings
      )
      if (modelMapping) {
        credentials.model_mapping = modelMapping
        credentialsChanged = true
      }
    }
  }

  if (input.enableCustomErrorCodes) {
    credentials.custom_error_codes_enabled = true
    credentials.custom_error_codes = [...input.selectedErrorCodes]
    credentialsChanged = true
  }

  if (input.enableInterceptWarmup) {
    credentials.intercept_warmup_requests = input.interceptWarmupRequests
    credentialsChanged = true
  }

  if (input.enableOpenAIPassthrough) {
    extra.openai_passthrough = input.openAIPassthroughEnabled
    // Keep backward compatibility key aligned.
    extra.openai_oauth_passthrough = input.openAIPassthroughEnabled
    extraChanged = true
  }

  if (input.enableOpenAIWSMode) {
    if (input.scopeType === 'oauth') {
      extra.openai_oauth_responses_websockets_v2_mode = input.openAIWSMode
      extra.openai_oauth_responses_websockets_v2_enabled = isOpenAIWSModeEnabled(
        input.openAIWSMode
      )
      extraChanged = true
    } else if (input.scopeType === 'apikey') {
      extra.openai_apikey_responses_websockets_v2_mode = input.openAIWSMode
      extra.openai_apikey_responses_websockets_v2_enabled = isOpenAIWSModeEnabled(
        input.openAIWSMode
      )
      extraChanged = true
    }
  }

  if (input.enableCodexCLIOnly) {
    extra.codex_cli_only = input.codexCLIOnlyEnabled
    extraChanged = true
  }

  if (input.enableAnthropicPassthrough) {
    extra.anthropic_passthrough = input.anthropicPassthroughEnabled
    extraChanged = true
  }

  if (credentialsChanged) {
    updates.credentials = credentials
  }

  if (extraChanged) {
    updates.extra = extra
  }

  return Object.keys(updates).length > 0 ? updates : null
}
