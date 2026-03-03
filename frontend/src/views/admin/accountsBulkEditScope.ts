export interface BulkEditScopeAccountCandidate {
  id: number
  platform: string
  type: string
}

export interface BulkEditSelectOption {
  value: string
  label: string
  [key: string]: unknown
}

export interface BulkEditScopeGroupedStat {
  key: string
  platform: string
  type: string
  count: number
}

export const buildBulkEditPlatformOptions = (
  candidates: BulkEditScopeAccountCandidate[],
  choosePlatformLabel: string,
  resolvePlatformLabel: (platform: string) => string,
  resolvePlatformOptionMeta?: (
    platform: string,
    count: number
  ) => Partial<BulkEditSelectOption> | null | undefined
): BulkEditSelectOption[] => {
  const platformCountMap = new Map<string, number>()
  for (const account of candidates) {
    platformCountMap.set(account.platform, (platformCountMap.get(account.platform) ?? 0) + 1)
  }

  return [
    { value: '', label: choosePlatformLabel },
    ...Array.from(platformCountMap.entries()).map(([platform, count]) => ({
      value: platform,
      label: resolvePlatformLabel(platform),
      ...(resolvePlatformOptionMeta?.(platform, count) ?? {})
    }))
  ]
}

export const buildBulkEditTypeOptions = (
  candidates: BulkEditScopeAccountCandidate[],
  platform: string,
  chooseTypeLabel: string,
  resolveTypeLabel: (type: string) => string,
  resolveTypeOptionMeta?: (
    accountType: string,
    count: number
  ) => Partial<BulkEditSelectOption> | null | undefined
): BulkEditSelectOption[] => {
  if (!platform) {
    return [{ value: '', label: chooseTypeLabel }]
  }

  const typeCountMap = new Map<string, number>()
  for (const account of candidates) {
    if (account.platform !== platform) continue
    typeCountMap.set(account.type, (typeCountMap.get(account.type) ?? 0) + 1)
  }

  return [
    { value: '', label: chooseTypeLabel },
    ...Array.from(typeCountMap.entries()).map(([accountType, count]) => ({
      value: accountType,
      label: resolveTypeLabel(accountType),
      ...(resolveTypeOptionMeta?.(accountType, count) ?? {})
    }))
  ]
}

export const matchBulkEditScopedAccountIds = (
  candidates: BulkEditScopeAccountCandidate[],
  platform: string,
  type: string
): number[] =>
  candidates
    .filter((account) => account.platform === platform && account.type === type)
    .map((account) => account.id)

export const countBulkEditScopedAccounts = (
  candidates: BulkEditScopeAccountCandidate[],
  platform: string,
  type: string
): number => matchBulkEditScopedAccountIds(candidates, platform, type).length

export const buildBulkEditScopeGroupedStats = (
  candidates: BulkEditScopeAccountCandidate[]
): BulkEditScopeGroupedStat[] => {
  const countMap = new Map<string, BulkEditScopeGroupedStat>()
  for (const account of candidates) {
    const key = `${account.platform}:${account.type}`
    const existing = countMap.get(key)
    if (existing) {
      existing.count += 1
      continue
    }
    countMap.set(key, {
      key,
      platform: account.platform,
      type: account.type,
      count: 1
    })
  }

  return Array.from(countMap.values()).sort((a, b) => {
    if (a.platform === b.platform) {
      return a.type.localeCompare(b.type)
    }
    return a.platform.localeCompare(b.platform)
  })
}
