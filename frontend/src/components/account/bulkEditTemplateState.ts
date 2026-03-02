import {
  OPENAI_WS_MODE_OFF,
  normalizeOpenAIWSMode,
  type OpenAIWSMode
} from '@/utils/openaiWsMode'
import type { BulkEditModelMapping } from './bulkEditPayload'

export interface BulkEditTemplateState {
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

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null

const toBoolean = (value: unknown, fallback: boolean): boolean =>
  typeof value === 'boolean' ? value : fallback

const toStringValue = (value: unknown, fallback: string): string =>
  typeof value === 'string' ? value : fallback

const toPositiveInteger = (value: unknown, fallback: number): number => {
  if (typeof value !== 'number' || !Number.isFinite(value)) return fallback
  return Math.max(1, Math.floor(value))
}

const toRateMultiplier = (value: unknown, fallback: number): number => {
  if (typeof value !== 'number' || !Number.isFinite(value)) return fallback
  return Math.max(0, value)
}

const toNumberList = (value: unknown): number[] => {
  if (!Array.isArray(value)) return []
  return value
    .filter((item): item is number => typeof item === 'number' && Number.isFinite(item))
    .map((item) => Math.floor(item))
}

const toStringList = (value: unknown): string[] => {
  if (!Array.isArray(value)) return []
  return value.filter((item): item is string => typeof item === 'string')
}

const toModelMappings = (value: unknown): BulkEditModelMapping[] => {
  if (!Array.isArray(value)) return []
  const next: BulkEditModelMapping[] = []
  for (const item of value) {
    if (!isRecord(item)) continue
    if (typeof item.from !== 'string' || typeof item.to !== 'string') continue
    next.push({ from: item.from, to: item.to })
  }
  return next
}

const toStatus = (value: unknown): 'active' | 'inactive' => {
  return value === 'inactive' ? 'inactive' : 'active'
}

const toModelRestrictionMode = (value: unknown): 'whitelist' | 'mapping' => {
  return value === 'mapping' ? 'mapping' : 'whitelist'
}

const toProxyID = (value: unknown): number | null => {
  if (value === null) return null
  if (typeof value !== 'number' || !Number.isFinite(value)) return null
  return value > 0 ? Math.floor(value) : null
}

export const createDefaultBulkEditTemplateState = (): BulkEditTemplateState => ({
  enableBaseUrl: false,
  enableModelRestriction: false,
  enableCustomErrorCodes: false,
  enableInterceptWarmup: false,
  enableOpenAIPassthrough: false,
  enableOpenAIWSMode: false,
  enableCodexCLIOnly: false,
  enableAnthropicPassthrough: false,
  enableProxy: false,
  enableConcurrency: false,
  enablePriority: false,
  enableRateMultiplier: false,
  enableStatus: false,
  enableGroups: false,
  baseUrl: '',
  modelRestrictionMode: 'whitelist',
  allowedModels: [],
  modelMappings: [],
  selectedErrorCodes: [],
  interceptWarmupRequests: false,
  openAIPassthroughEnabled: false,
  openAIWSMode: OPENAI_WS_MODE_OFF,
  codexCLIOnlyEnabled: false,
  anthropicPassthroughEnabled: false,
  proxyId: null,
  concurrency: 1,
  priority: 1,
  rateMultiplier: 1,
  status: 'active',
  groupIds: []
})

export const normalizeBulkEditTemplateState = (value: unknown): BulkEditTemplateState => {
  const defaults = createDefaultBulkEditTemplateState()
  if (!isRecord(value)) return defaults

  const normalizedWSMode = normalizeOpenAIWSMode(value.openAIWSMode)

  return {
    enableBaseUrl: toBoolean(value.enableBaseUrl, defaults.enableBaseUrl),
    enableModelRestriction: toBoolean(
      value.enableModelRestriction,
      defaults.enableModelRestriction
    ),
    enableCustomErrorCodes: toBoolean(
      value.enableCustomErrorCodes,
      defaults.enableCustomErrorCodes
    ),
    enableInterceptWarmup: toBoolean(
      value.enableInterceptWarmup,
      defaults.enableInterceptWarmup
    ),
    enableOpenAIPassthrough: toBoolean(
      value.enableOpenAIPassthrough,
      defaults.enableOpenAIPassthrough
    ),
    enableOpenAIWSMode: toBoolean(value.enableOpenAIWSMode, defaults.enableOpenAIWSMode),
    enableCodexCLIOnly: toBoolean(value.enableCodexCLIOnly, defaults.enableCodexCLIOnly),
    enableAnthropicPassthrough: toBoolean(
      value.enableAnthropicPassthrough,
      defaults.enableAnthropicPassthrough
    ),
    enableProxy: toBoolean(value.enableProxy, defaults.enableProxy),
    enableConcurrency: toBoolean(value.enableConcurrency, defaults.enableConcurrency),
    enablePriority: toBoolean(value.enablePriority, defaults.enablePriority),
    enableRateMultiplier: toBoolean(value.enableRateMultiplier, defaults.enableRateMultiplier),
    enableStatus: toBoolean(value.enableStatus, defaults.enableStatus),
    enableGroups: toBoolean(value.enableGroups, defaults.enableGroups),
    baseUrl: toStringValue(value.baseUrl, defaults.baseUrl),
    modelRestrictionMode: toModelRestrictionMode(value.modelRestrictionMode),
    allowedModels: toStringList(value.allowedModels),
    modelMappings: toModelMappings(value.modelMappings),
    selectedErrorCodes: toNumberList(value.selectedErrorCodes),
    interceptWarmupRequests: toBoolean(
      value.interceptWarmupRequests,
      defaults.interceptWarmupRequests
    ),
    openAIPassthroughEnabled: toBoolean(
      value.openAIPassthroughEnabled,
      defaults.openAIPassthroughEnabled
    ),
    openAIWSMode: normalizedWSMode ?? defaults.openAIWSMode,
    codexCLIOnlyEnabled: toBoolean(value.codexCLIOnlyEnabled, defaults.codexCLIOnlyEnabled),
    anthropicPassthroughEnabled: toBoolean(
      value.anthropicPassthroughEnabled,
      defaults.anthropicPassthroughEnabled
    ),
    proxyId: toProxyID(value.proxyId),
    concurrency: toPositiveInteger(value.concurrency, defaults.concurrency),
    priority: toPositiveInteger(value.priority, defaults.priority),
    rateMultiplier: toRateMultiplier(value.rateMultiplier, defaults.rateMultiplier),
    status: toStatus(value.status),
    groupIds: toNumberList(value.groupIds)
  }
}

export const createBulkEditTemplateStateSnapshot = (
  state: BulkEditTemplateState
): BulkEditTemplateState => normalizeBulkEditTemplateState(state)
