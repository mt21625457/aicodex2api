import { describe, expect, it } from 'vitest'

import {
  applyUsageServiceTierMultiplier,
  calculateUsageAccountBilledCost,
  formatUsageServiceTier,
  getUsageServiceTierMultiplier,
  getStoredUsageServiceTierMultiplier,
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

  it('applies the billing multiplier on top of the base cost', () => {
    expect(applyUsageServiceTierMultiplier(1.25, 'fast')).toBe(2.5)
    expect(applyUsageServiceTierMultiplier(1.25, 'priority')).toBe(2.5)
    expect(applyUsageServiceTierMultiplier(1.25, 'flex')).toBe(0.625)
    expect(applyUsageServiceTierMultiplier(1.25, 'standard')).toBe(1.25)
    expect(applyUsageServiceTierMultiplier(undefined, 'fast')).toBe(0)
  })

  it('detects whether stored totals already include the service tier', () => {
    expect(getStoredUsageServiceTierMultiplier(1, 1, 1, 'fast')).toBe(1)
    expect(getStoredUsageServiceTierMultiplier(1, 2, 1, 'fast')).toBe(2)
    expect(getStoredUsageServiceTierMultiplier(1, 0.5, 1, 'flex')).toBe(0.5)
    expect(getStoredUsageServiceTierMultiplier(undefined, 2, 1, 'fast')).toBe(2)
    expect(getStoredUsageServiceTierMultiplier(1, undefined, 1, 'fast')).toBe(2)
    expect(getStoredUsageServiceTierMultiplier(1, 2, 0, 'fast')).toBe(2)
    expect(getStoredUsageServiceTierMultiplier(1, 1, undefined, 'standard')).toBe(1)
  })

  it('calculates account billed cost without double-applying old records', () => {
    expect(calculateUsageAccountBilledCost(1, 1, 1, 1.5, 'fast')).toBe(1.5)
    expect(calculateUsageAccountBilledCost(1, 2, 1, 1.5, 'fast')).toBe(3)
    expect(calculateUsageAccountBilledCost(1, 2, 1, undefined, 'fast')).toBe(2)
    expect(calculateUsageAccountBilledCost(1, 2, 1, -1, 'fast')).toBe(2)
  })
})
