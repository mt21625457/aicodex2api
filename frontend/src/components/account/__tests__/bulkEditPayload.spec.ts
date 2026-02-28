import { describe, expect, it, vi } from 'vitest'
import {
  OPENAI_WS_MODE_OFF,
  OPENAI_WS_MODE_SHARED,
  OPENAI_WS_MODE_DEDICATED
} from '@/utils/openaiWsMode'
import {
  buildBulkEditUpdatePayload,
  hasAnyBulkEditFieldEnabled,
  type BulkEditPayloadInput
} from '../bulkEditPayload'

const createInput = (overrides: Partial<BulkEditPayloadInput> = {}): BulkEditPayloadInput => ({
  scopeType: 'oauth',
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
  groupIds: [],
  ...overrides
})

describe('hasAnyBulkEditFieldEnabled', () => {
  it('returns false when all toggles are disabled', () => {
    expect(hasAnyBulkEditFieldEnabled(createInput())).toBe(false)
  })

  it('returns true when at least one toggle is enabled', () => {
    expect(hasAnyBulkEditFieldEnabled(createInput({ enableOpenAIWSMode: true }))).toBe(true)
  })
})

describe('buildBulkEditUpdatePayload', () => {
  it('returns null when no field is enabled', () => {
    expect(buildBulkEditUpdatePayload(createInput())).toBeNull()
  })

  it('builds base fields and supports clearing proxy with 0', () => {
    const payload = buildBulkEditUpdatePayload(
      createInput({
        enableProxy: true,
        proxyId: null,
        enableConcurrency: true,
        concurrency: 8,
        enablePriority: true,
        priority: 9,
        enableRateMultiplier: true,
        rateMultiplier: 1.25,
        enableStatus: true,
        status: 'inactive',
        enableGroups: true,
        groupIds: [2, 3]
      })
    )

    expect(payload).toEqual({
      proxy_id: 0,
      concurrency: 8,
      priority: 9,
      rate_multiplier: 1.25,
      status: 'inactive',
      group_ids: [2, 3]
    })
  })

  it('trims base_url and ignores empty input', () => {
    const withValue = buildBulkEditUpdatePayload(
      createInput({
        enableBaseUrl: true,
        baseUrl: '  https://api.example.com/v1  '
      })
    )
    expect(withValue).toEqual({
      credentials: {
        base_url: 'https://api.example.com/v1'
      }
    })

    const withEmpty = buildBulkEditUpdatePayload(
      createInput({
        enableBaseUrl: true,
        baseUrl: '   '
      })
    )
    expect(withEmpty).toBeNull()
  })

  it('builds model whitelist mapping', () => {
    const payload = buildBulkEditUpdatePayload(
      createInput({
        enableModelRestriction: true,
        modelRestrictionMode: 'whitelist',
        allowedModels: ['claude-sonnet-4-6', 'gpt-5.2-codex']
      })
    )

    expect(payload).toEqual({
      credentials: {
        model_mapping: {
          'claude-sonnet-4-6': 'claude-sonnet-4-6',
          'gpt-5.2-codex': 'gpt-5.2-codex'
        }
      }
    })
  })

  it('builds model mapping mode and filters invalid rules', () => {
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})
    try {
      const payload = buildBulkEditUpdatePayload(
        createInput({
          enableModelRestriction: true,
          modelRestrictionMode: 'mapping',
          modelMappings: [
            { from: '', to: 'ignored' },
            { from: 'bad*wild', to: 'x' },
            { from: 'claude-*', to: 'claude-sonnet-4-6' },
            { from: 'gpt-5.2-codex', to: 'gpt-*' },
            { from: 'gpt-5.1-codex', to: 'gpt-5.2-codex' }
          ]
        })
      )

      expect(payload).toEqual({
        credentials: {
          model_mapping: {
            'claude-*': 'claude-sonnet-4-6',
            'gpt-5.1-codex': 'gpt-5.2-codex'
          }
        }
      })
    } finally {
      warnSpy.mockRestore()
    }
  })

  it('writes custom error codes and warmup interception into credentials', () => {
    const payload = buildBulkEditUpdatePayload(
      createInput({
        enableCustomErrorCodes: true,
        selectedErrorCodes: [429, 503],
        enableInterceptWarmup: true,
        interceptWarmupRequests: true
      })
    )

    expect(payload).toEqual({
      credentials: {
        custom_error_codes_enabled: true,
        custom_error_codes: [429, 503],
        intercept_warmup_requests: true
      }
    })
  })

  it('writes OpenAI passthrough and OAuth ws mode keys', () => {
    const payload = buildBulkEditUpdatePayload(
      createInput({
        scopeType: 'oauth',
        enableOpenAIPassthrough: true,
        openAIPassthroughEnabled: true,
        enableOpenAIWSMode: true,
        openAIWSMode: OPENAI_WS_MODE_SHARED
      })
    )

    expect(payload).toEqual({
      extra: {
        openai_passthrough: true,
        openai_oauth_passthrough: true,
        openai_oauth_responses_websockets_v2_mode: OPENAI_WS_MODE_SHARED,
        openai_oauth_responses_websockets_v2_enabled: true
      }
    })
  })

  it('writes API key ws mode keys and off-mode disabled flag', () => {
    const payload = buildBulkEditUpdatePayload(
      createInput({
        scopeType: 'apikey',
        enableOpenAIWSMode: true,
        openAIWSMode: OPENAI_WS_MODE_OFF
      })
    )

    expect(payload).toEqual({
      extra: {
        openai_apikey_responses_websockets_v2_mode: OPENAI_WS_MODE_OFF,
        openai_apikey_responses_websockets_v2_enabled: false
      }
    })
  })

  it('ignores ws mode when scope type is not oauth/apikey', () => {
    const payload = buildBulkEditUpdatePayload(
      createInput({
        scopeType: 'setup-token',
        enableOpenAIWSMode: true,
        openAIWSMode: OPENAI_WS_MODE_DEDICATED
      })
    )

    expect(payload).toBeNull()
  })

  it('writes codex and anthropic passthrough flags', () => {
    const payload = buildBulkEditUpdatePayload(
      createInput({
        enableCodexCLIOnly: true,
        codexCLIOnlyEnabled: true,
        enableAnthropicPassthrough: true,
        anthropicPassthroughEnabled: false
      })
    )

    expect(payload).toEqual({
      extra: {
        codex_cli_only: true,
        anthropic_passthrough: false
      }
    })
  })
})
