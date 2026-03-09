export function normalizeUsageServiceTier(serviceTier?: string | null): string | null {
  const value = serviceTier?.trim().toLowerCase()
  if (!value) return null
  if (value === 'fast') return 'priority'
  if (value === 'default' || value === 'standard') return 'standard'
  if (value === 'priority' || value === 'flex') return value
  return value
}

export function formatUsageServiceTier(serviceTier?: string | null): string {
  const normalized = normalizeUsageServiceTier(serviceTier)
  if (!normalized) return 'standard'
  return normalized
}

export function getUsageServiceTierMultiplier(serviceTier?: string | null): number {
  const normalized = formatUsageServiceTier(serviceTier)
  if (normalized === 'priority') return 2
  if (normalized === 'flex') return 0.5
  return 1
}
