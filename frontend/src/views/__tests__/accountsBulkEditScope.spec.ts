import { describe, expect, it } from 'vitest'
import {
  buildBulkEditPlatformOptions,
  buildBulkEditScopeGroupedStats,
  buildBulkEditTypeOptions,
  countBulkEditScopedAccounts,
  matchBulkEditScopedAccountIds
} from '../admin/accountsBulkEditScope'

const candidates = [
  { id: 1, platform: 'openai', type: 'oauth' },
  { id: 2, platform: 'openai', type: 'apikey' },
  { id: 3, platform: 'openai', type: 'oauth' },
  { id: 4, platform: 'anthropic', type: 'apikey' },
  { id: 5, platform: 'gemini', type: 'oauth' }
]

describe('accountsBulkEditScope helpers', () => {
  it('builds platform options with default item and unique platforms', () => {
    const options = buildBulkEditPlatformOptions(
      candidates,
      '请选择平台',
      (platform) => `label:${platform}`
    )

    expect(options).toEqual([
      { value: '', label: '请选择平台' },
      { value: 'openai', label: 'label:openai' },
      { value: 'anthropic', label: 'label:anthropic' },
      { value: 'gemini', label: 'label:gemini' }
    ])
  })

  it('returns only default type option when platform is empty', () => {
    const options = buildBulkEditTypeOptions(candidates, '', '请选择类型', (type) => type)
    expect(options).toEqual([{ value: '', label: '请选择类型' }])
  })

  it('builds type options for selected platform with deduplication', () => {
    const options = buildBulkEditTypeOptions(
      candidates,
      'openai',
      '请选择类型',
      (type) => `type:${type}`
    )

    expect(options).toEqual([
      { value: '', label: '请选择类型' },
      { value: 'oauth', label: 'type:oauth' },
      { value: 'apikey', label: 'type:apikey' }
    ])
  })

  it('supports option meta builders for platform/type options', () => {
    const platformOptions = buildBulkEditPlatformOptions(
      candidates,
      '请选择平台',
      (platform) => platform,
      (_platform, count) => ({ label: `count:${count}` })
    )
    expect(platformOptions).toEqual([
      { value: '', label: '请选择平台' },
      { value: 'openai', label: 'count:3' },
      { value: 'anthropic', label: 'count:1' },
      { value: 'gemini', label: 'count:1' }
    ])

    const typeOptions = buildBulkEditTypeOptions(
      candidates,
      'openai',
      '请选择类型',
      (type) => type,
      (type, count) => ({
        label: `${type} (${count})`,
        disabled: type === 'apikey'
      })
    )
    expect(typeOptions).toEqual([
      { value: '', label: '请选择类型' },
      { value: 'oauth', label: 'oauth (2)', disabled: false },
      { value: 'apikey', label: 'apikey (1)', disabled: true }
    ])
  })

  it('matches scoped account IDs and count', () => {
    expect(matchBulkEditScopedAccountIds(candidates, 'openai', 'oauth')).toEqual([1, 3])
    expect(countBulkEditScopedAccounts(candidates, 'openai', 'oauth')).toBe(2)
    expect(matchBulkEditScopedAccountIds(candidates, 'sora', 'apikey')).toEqual([])
    expect(countBulkEditScopedAccounts(candidates, 'sora', 'apikey')).toBe(0)
  })

  it('builds grouped stats for scope preview', () => {
    expect(buildBulkEditScopeGroupedStats(candidates)).toEqual([
      { key: 'anthropic:apikey', platform: 'anthropic', type: 'apikey', count: 1 },
      { key: 'gemini:oauth', platform: 'gemini', type: 'oauth', count: 1 },
      { key: 'openai:apikey', platform: 'openai', type: 'apikey', count: 1 },
      { key: 'openai:oauth', platform: 'openai', type: 'oauth', count: 2 }
    ])
  })
})
