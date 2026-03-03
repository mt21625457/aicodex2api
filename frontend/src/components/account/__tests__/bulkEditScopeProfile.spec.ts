import { describe, expect, it } from 'vitest'
import {
  resolveBulkEditScopeCapabilities,
  resolveBulkEditScopeEditorKey
} from '../bulkEditScopeProfile'

describe('resolveBulkEditScopeCapabilities', () => {
  it('returns OpenAI OAuth capabilities', () => {
    const profile = resolveBulkEditScopeCapabilities('openai', 'oauth')

    expect(profile.supportsBaseUrl).toBe(false)
    expect(profile.supportsModelRestriction).toBe(false)
    expect(profile.supportsCustomErrorCodes).toBe(false)
    expect(profile.supportsInterceptWarmup).toBe(false)
    expect(profile.supportsOpenAIPassthrough).toBe(true)
    expect(profile.supportsOpenAIWSMode).toBe(true)
    expect(profile.supportsCodexCLIOnly).toBe(true)
    expect(profile.supportsAnthropicPassthrough).toBe(false)
  })

  it('returns OpenAI API Key capabilities', () => {
    const profile = resolveBulkEditScopeCapabilities('openai', 'apikey')

    expect(profile.supportsBaseUrl).toBe(true)
    expect(profile.supportsModelRestriction).toBe(true)
    expect(profile.supportsCustomErrorCodes).toBe(true)
    expect(profile.supportsInterceptWarmup).toBe(false)
    expect(profile.supportsOpenAIPassthrough).toBe(true)
    expect(profile.supportsOpenAIWSMode).toBe(true)
    expect(profile.supportsCodexCLIOnly).toBe(false)
    expect(profile.supportsAnthropicPassthrough).toBe(false)
  })

  it('returns Anthropic API Key capabilities', () => {
    const profile = resolveBulkEditScopeCapabilities('anthropic', 'apikey')

    expect(profile.supportsBaseUrl).toBe(true)
    expect(profile.supportsModelRestriction).toBe(true)
    expect(profile.supportsCustomErrorCodes).toBe(true)
    expect(profile.supportsInterceptWarmup).toBe(true)
    expect(profile.supportsOpenAIPassthrough).toBe(false)
    expect(profile.supportsOpenAIWSMode).toBe(false)
    expect(profile.supportsCodexCLIOnly).toBe(false)
    expect(profile.supportsAnthropicPassthrough).toBe(true)
  })

  it('returns Antigravity Upstream capabilities', () => {
    const profile = resolveBulkEditScopeCapabilities('antigravity', 'upstream')

    expect(profile.supportsBaseUrl).toBe(true)
    expect(profile.supportsModelRestriction).toBe(false)
    expect(profile.supportsCustomErrorCodes).toBe(false)
    expect(profile.supportsInterceptWarmup).toBe(true)
    expect(profile.supportsOpenAIPassthrough).toBe(false)
    expect(profile.supportsOpenAIWSMode).toBe(false)
    expect(profile.supportsCodexCLIOnly).toBe(false)
    expect(profile.supportsAnthropicPassthrough).toBe(false)
  })
})

describe('resolveBulkEditScopeEditorKey', () => {
  it('resolves known scope keys', () => {
    expect(resolveBulkEditScopeEditorKey('openai', 'oauth')).toBe('openai:oauth')
    expect(resolveBulkEditScopeEditorKey('anthropic', 'setup-token')).toBe(
      'anthropic:setup-token'
    )
    expect(resolveBulkEditScopeEditorKey('antigravity', 'upstream')).toBe(
      'antigravity:upstream'
    )
  })

  it('returns null for unsupported or incomplete scope', () => {
    expect(resolveBulkEditScopeEditorKey('openai', 'upstream')).toBeNull()
    expect(resolveBulkEditScopeEditorKey('sora', 'setup-token')).toBeNull()
    expect(resolveBulkEditScopeEditorKey('', 'oauth')).toBeNull()
    expect(resolveBulkEditScopeEditorKey('gemini', '')).toBeNull()
  })
})
