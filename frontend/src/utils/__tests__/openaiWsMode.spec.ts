import { describe, expect, it } from 'vitest'
import {
  OPENAI_WS_MODE_CTX_POOL,
  OPENAI_WS_MODE_OFF,
  OPENAI_WS_MODE_PASSTHROUGH,
  isOpenAIWSModeEnabled,
  normalizeOpenAIWSMode,
  openAIWSModeFromEnabled,
  resolveOpenAIWSModeConcurrencyHintKey,
  resolveOpenAIWSModeFromExtra
} from '@/utils/openaiWsMode'

describe('openaiWsMode utils', () => {
  it('normalizes mode values', () => {
    expect(normalizeOpenAIWSMode('off')).toBe(OPENAI_WS_MODE_OFF)
    expect(normalizeOpenAIWSMode('CTX_POOL')).toBe(OPENAI_WS_MODE_CTX_POOL)
    expect(normalizeOpenAIWSMode(' passthrough ')).toBe(OPENAI_WS_MODE_PASSTHROUGH)
    expect(normalizeOpenAIWSMode(' Shared ')).toBeNull()
    expect(normalizeOpenAIWSMode('DEDICATED')).toBeNull()
    expect(normalizeOpenAIWSMode('invalid')).toBeNull()
  })

  it('maps legacy enabled flag to mode', () => {
    expect(openAIWSModeFromEnabled(true)).toBe(OPENAI_WS_MODE_CTX_POOL)
    expect(openAIWSModeFromEnabled(false)).toBe(OPENAI_WS_MODE_OFF)
    expect(openAIWSModeFromEnabled('true')).toBeNull()
  })

  it('resolves by mode key first, then enabled, then fallback enabled keys', () => {
    const extra = {
      openai_oauth_responses_websockets_v2_mode: 'ctx_pool',
      openai_oauth_responses_websockets_v2_enabled: false,
      responses_websockets_v2_enabled: false
    }
    const mode = resolveOpenAIWSModeFromExtra(extra, {
      modeKey: 'openai_oauth_responses_websockets_v2_mode',
      enabledKey: 'openai_oauth_responses_websockets_v2_enabled',
      fallbackEnabledKeys: ['responses_websockets_v2_enabled', 'openai_ws_enabled']
    })
    expect(mode).toBe(OPENAI_WS_MODE_CTX_POOL)
  })

  it('falls back to default when nothing is present', () => {
    const mode = resolveOpenAIWSModeFromExtra({}, {
      modeKey: 'openai_apikey_responses_websockets_v2_mode',
      enabledKey: 'openai_apikey_responses_websockets_v2_enabled',
      fallbackEnabledKeys: ['responses_websockets_v2_enabled', 'openai_ws_enabled'],
      defaultMode: OPENAI_WS_MODE_OFF
    })
    expect(mode).toBe(OPENAI_WS_MODE_OFF)
  })

  it('treats off as disabled and non-off modes as enabled', () => {
    expect(isOpenAIWSModeEnabled(OPENAI_WS_MODE_OFF)).toBe(false)
    expect(isOpenAIWSModeEnabled(OPENAI_WS_MODE_CTX_POOL)).toBe(true)
    expect(isOpenAIWSModeEnabled(OPENAI_WS_MODE_PASSTHROUGH)).toBe(true)
  })

  it('resolves concurrency hint key by mode', () => {
    expect(resolveOpenAIWSModeConcurrencyHintKey(OPENAI_WS_MODE_OFF)).toBe('admin.accounts.openai.wsModeConcurrencyHint')
    expect(resolveOpenAIWSModeConcurrencyHintKey(OPENAI_WS_MODE_CTX_POOL)).toBe('admin.accounts.openai.wsModeConcurrencyHint')
    expect(resolveOpenAIWSModeConcurrencyHintKey(OPENAI_WS_MODE_PASSTHROUGH)).toBe(
      'admin.accounts.openai.wsModePassthroughHint'
    )
  })

  it('resolves passthrough from modeKey', () => {
    const extra = {
      openai_oauth_responses_websockets_v2_mode: 'passthrough',
      openai_oauth_responses_websockets_v2_enabled: true
    }
    const mode = resolveOpenAIWSModeFromExtra(extra, {
      modeKey: 'openai_oauth_responses_websockets_v2_mode',
      enabledKey: 'openai_oauth_responses_websockets_v2_enabled'
    })
    expect(mode).toBe(OPENAI_WS_MODE_PASSTHROUGH)
  })

  it('resolves passthrough from apikey modeKey', () => {
    const extra = {
      openai_apikey_responses_websockets_v2_mode: 'passthrough'
    }
    const mode = resolveOpenAIWSModeFromExtra(extra, {
      modeKey: 'openai_apikey_responses_websockets_v2_mode',
      enabledKey: 'openai_apikey_responses_websockets_v2_enabled'
    })
    expect(mode).toBe(OPENAI_WS_MODE_PASSTHROUGH)
  })

  it('resolves enabled fallback when mode key is absent', () => {
    const extra = {
      openai_oauth_responses_websockets_v2_enabled: true
    }
    const mode = resolveOpenAIWSModeFromExtra(extra, {
      modeKey: 'openai_oauth_responses_websockets_v2_mode',
      enabledKey: 'openai_oauth_responses_websockets_v2_enabled'
    })
    expect(mode).toBe(OPENAI_WS_MODE_CTX_POOL)
  })

  it('resolves disabled fallback when enabled is false', () => {
    const extra = {
      openai_oauth_responses_websockets_v2_enabled: false
    }
    const mode = resolveOpenAIWSModeFromExtra(extra, {
      modeKey: 'openai_oauth_responses_websockets_v2_mode',
      enabledKey: 'openai_oauth_responses_websockets_v2_enabled'
    })
    expect(mode).toBe(OPENAI_WS_MODE_OFF)
  })
})
