import { describe, expect, it } from 'vitest'
import { OPENAI_WS_MODE_CTX_POOL } from '@/utils/openaiWsMode'
import {
  createBulkEditTemplateStateSnapshot,
  createDefaultBulkEditTemplateState,
  normalizeBulkEditTemplateState
} from '../bulkEditTemplateState'

describe('bulkEditTemplateState', () => {
  it('builds default state', () => {
    const state = createDefaultBulkEditTemplateState()
    expect(state.enableBaseUrl).toBe(false)
    expect(state.openAIWSMode).toBe('off')
    expect(state.modelMappings).toEqual([])
    expect(state.groupIds).toEqual([])
  })

  it('normalizes invalid input to defaults', () => {
    const state = normalizeBulkEditTemplateState(null)
    expect(state).toEqual(createDefaultBulkEditTemplateState())
  })

  it('normalizes and sanitizes mixed payload', () => {
    const state = normalizeBulkEditTemplateState({
      enableBaseUrl: true,
      baseUrl: 'https://api.example.com',
      modelRestrictionMode: 'mapping',
      allowedModels: ['a', 1, 'b'],
      modelMappings: [{ from: 'x', to: 'y' }, { from: 'bad' }, 'bad-item'],
      selectedErrorCodes: [429, '503', 529.8],
      openAIWSMode: 'ctx_pool',
      proxyId: 18.9,
      concurrency: 0,
      priority: 9.4,
      rateMultiplier: -2,
      status: 'inactive',
      groupIds: [1, 2.7, '3']
    })

    expect(state.enableBaseUrl).toBe(true)
    expect(state.baseUrl).toBe('https://api.example.com')
    expect(state.modelRestrictionMode).toBe('mapping')
    expect(state.allowedModels).toEqual(['a', 'b'])
    expect(state.modelMappings).toEqual([{ from: 'x', to: 'y' }])
    expect(state.selectedErrorCodes).toEqual([429, 529])
    expect(state.openAIWSMode).toBe('ctx_pool')
    expect(state.proxyId).toBe(18)
    expect(state.concurrency).toBe(1)
    expect(state.priority).toBe(9)
    expect(state.rateMultiplier).toBe(0)
    expect(state.status).toBe('inactive')
    expect(state.groupIds).toEqual([1, 2])
  })

  it('falls back for invalid ws mode and proxy id', () => {
    const state = normalizeBulkEditTemplateState({
      openAIWSMode: 'invalid-mode',
      proxyId: 0
    })
    expect(state.openAIWSMode).toBe('off')
    expect(state.proxyId).toBeNull()
  })

  it('creates snapshot as deep-normalized clone', () => {
    const source = createDefaultBulkEditTemplateState()
    source.openAIWSMode = OPENAI_WS_MODE_CTX_POOL
    source.allowedModels.push('gpt-5.2-codex')
    source.modelMappings.push({ from: 'a', to: 'b' })
    source.groupIds.push(9)

    const snapshot = createBulkEditTemplateStateSnapshot(source)
    source.allowedModels[0] = 'mutated'
    source.modelMappings[0].to = 'changed'
    source.groupIds[0] = 0

    expect(snapshot.openAIWSMode).toBe('ctx_pool')
    expect(snapshot.allowedModels).toEqual(['gpt-5.2-codex'])
    expect(snapshot.modelMappings).toEqual([{ from: 'a', to: 'b' }])
    expect(snapshot.groupIds).toEqual([9])
  })
})
