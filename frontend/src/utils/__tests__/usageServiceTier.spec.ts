import { describe, expect, it } from 'vitest'

import {
  formatUsageServiceTier,
  getUsageServiceTierMultiplier,
  normalizeUsageServiceTier
} from '../usageServiceTier'

describe('usageServiceTier', () => {
  it('normalizes fast to priority', () => {
    expect(normalizeUsageServiceTier('fast')).toBe('priority')
    expect(normalizeUsageServiceTier(' Priority ')).toBe('priority')
  })

  it('formats empty and default as standard', () => {
    expect(formatUsageServiceTier(undefined)).toBe('standard')
    expect(formatUsageServiceTier(null)).toBe('standard')
    expect(formatUsageServiceTier('')).toBe('standard')
    expect(formatUsageServiceTier('default')).toBe('standard')
    expect(formatUsageServiceTier('standard')).toBe('standard')
  })

  it('preserves known and unknown values', () => {
    expect(formatUsageServiceTier('priority')).toBe('priority')
    expect(formatUsageServiceTier('flex')).toBe('flex')
    expect(formatUsageServiceTier('economy')).toBe('economy')
  })

  it('returns the expected billing multiplier for service tiers', () => {
    expect(getUsageServiceTierMultiplier('fast')).toBe(2)
    expect(getUsageServiceTierMultiplier('priority')).toBe(2)
    expect(getUsageServiceTierMultiplier('flex')).toBe(0.5)
    expect(getUsageServiceTierMultiplier('standard')).toBe(1)
    expect(getUsageServiceTierMultiplier(undefined)).toBe(1)
  })
})
