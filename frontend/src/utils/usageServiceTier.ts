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

export function applyUsageServiceTierMultiplier(
  cost: number | null | undefined,
  serviceTier?: string | null
): number {
  if (typeof cost !== 'number' || !Number.isFinite(cost)) return 0
  return cost * getUsageServiceTierMultiplier(serviceTier)
}

export function getStoredUsageServiceTierMultiplier(
  totalCost: number | null | undefined,
  actualCost: number | null | undefined,
  rateMultiplier: number | null | undefined,
  serviceTier?: string | null
): number {
  const tierMultiplier = getUsageServiceTierMultiplier(serviceTier)
  if (tierMultiplier === 1) return 1
  if (typeof totalCost !== 'number' || !Number.isFinite(totalCost) || totalCost <= 0) {
    return tierMultiplier
  }
  if (typeof actualCost !== 'number' || !Number.isFinite(actualCost)) {
    return tierMultiplier
  }
  const effectiveRate = typeof rateMultiplier === 'number' && Number.isFinite(rateMultiplier) && rateMultiplier > 0
    ? rateMultiplier
    : 1
  const expectedOldActual = totalCost * effectiveRate
  return Math.abs(actualCost - expectedOldActual) < 1e-6 ? 1 : tierMultiplier
}

export function calculateUsageAccountBilledCost(
  totalCost: number | null | undefined,
  actualCost: number | null | undefined,
  rateMultiplier: number | null | undefined,
  accountRateMultiplier: number | null | undefined,
  serviceTier?: string | null
): number {
  const baseCost = typeof totalCost === 'number' && Number.isFinite(totalCost) ? totalCost : 0
  const accountRate = typeof accountRateMultiplier === 'number' && Number.isFinite(accountRateMultiplier) && accountRateMultiplier >= 0
    ? accountRateMultiplier
    : 1
  return baseCost * getStoredUsageServiceTierMultiplier(totalCost, actualCost, rateMultiplier, serviceTier) * accountRate
}
