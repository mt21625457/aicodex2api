import type { AccountPlatform, AccountType } from '@/types'

export interface BulkEditScopeCapabilities {
  supportsBaseUrl: boolean
  supportsModelRestriction: boolean
  supportsCustomErrorCodes: boolean
  supportsInterceptWarmup: boolean
  supportsOpenAIPassthrough: boolean
  supportsOpenAIWSMode: boolean
  supportsCodexCLIOnly: boolean
  supportsAnthropicPassthrough: boolean
}

export const BULK_EDIT_SCOPE_EDITOR_KEYS = [
  'anthropic:oauth',
  'anthropic:setup-token',
  'anthropic:apikey',
  'openai:oauth',
  'openai:apikey',
  'gemini:oauth',
  'gemini:apikey',
  'antigravity:oauth',
  'antigravity:upstream',
  'sora:oauth',
  'sora:apikey'
] as const

export type BulkEditScopeEditorKey = (typeof BULK_EDIT_SCOPE_EDITOR_KEYS)[number]

const bulkEditScopeEditorKeySet = new Set<string>(BULK_EDIT_SCOPE_EDITOR_KEYS)

const isOpenAIScope = (platform?: AccountPlatform | '' | null) => platform === 'openai'
const isAnthropicScope = (platform?: AccountPlatform | '' | null) => platform === 'anthropic'
const isAnthropicOrAntigravityScope = (platform?: AccountPlatform | '' | null) =>
  platform === 'anthropic' || platform === 'antigravity'
const isAPIKeyScope = (type?: AccountType | '' | null) => type === 'apikey'
const isOpenAIOAuthScope = (
  platform?: AccountPlatform | '' | null,
  type?: AccountType | '' | null
) => isOpenAIScope(platform) && type === 'oauth'
const isOpenAIOAuthOrAPIKeyScope = (
  platform?: AccountPlatform | '' | null,
  type?: AccountType | '' | null
) => isOpenAIScope(platform) && (type === 'oauth' || type === 'apikey')

export const resolveBulkEditScopeEditorKey = (
  platform?: AccountPlatform | '' | null,
  type?: AccountType | '' | null
): BulkEditScopeEditorKey | null => {
  if (!platform || !type) return null
  const key = `${platform}:${type}` as BulkEditScopeEditorKey
  return bulkEditScopeEditorKeySet.has(key) ? key : null
}

export const resolveBulkEditScopeCapabilities = (
  platform?: AccountPlatform | '' | null,
  type?: AccountType | '' | null
): BulkEditScopeCapabilities => {
  return {
    supportsBaseUrl: type === 'apikey' || type === 'upstream',
    supportsModelRestriction: isAPIKeyScope(type) && platform !== 'antigravity',
    supportsCustomErrorCodes: isAPIKeyScope(type),
    supportsInterceptWarmup: isAnthropicOrAntigravityScope(platform),
    supportsOpenAIPassthrough: isOpenAIOAuthOrAPIKeyScope(platform, type),
    supportsOpenAIWSMode: isOpenAIOAuthOrAPIKeyScope(platform, type),
    supportsCodexCLIOnly: isOpenAIOAuthScope(platform, type),
    supportsAnthropicPassthrough: isAnthropicScope(platform) && isAPIKeyScope(type)
  }
}
