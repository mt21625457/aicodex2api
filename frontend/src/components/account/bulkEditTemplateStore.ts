import type { AccountPlatform, AccountType } from '@/types'

export const BULK_EDIT_TEMPLATES_STORAGE_KEY = 'admin.bulk_edit_templates.v1'
export type BulkEditTemplateShareScope = 'private' | 'team' | 'groups'

export const normalizeBulkEditTemplateShareScope = (
  value: unknown
): BulkEditTemplateShareScope => {
  if (value === 'team') return 'team'
  if (value === 'groups') return 'groups'
  return 'private'
}

export const normalizeBulkEditTemplateGroupIDs = (value: unknown): number[] => {
  if (!Array.isArray(value)) return []
  const seen = new Set<number>()
  const next: number[] = []
  for (const item of value) {
    if (typeof item !== 'number' || !Number.isFinite(item) || item <= 0) continue
    const normalized = Math.floor(item)
    if (seen.has(normalized)) continue
    seen.add(normalized)
    next.push(normalized)
  }
  return next.sort((a, b) => a - b)
}

export interface BulkEditTemplateRecord<TState = Record<string, unknown>> {
  id: string
  name: string
  scopePlatform: AccountPlatform | ''
  scopeType: AccountType | ''
  shareScope: BulkEditTemplateShareScope
  groupIds: number[]
  state: TState
  updatedAt: number
  ownerUserId?: number | null
}

export const parseBulkEditTemplateRecords = <TState = Record<string, unknown>>(
  raw: string | null | undefined
): BulkEditTemplateRecord<TState>[] => {
  if (!raw) return []

  try {
    const parsed = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    const records: BulkEditTemplateRecord<TState>[] = []
    for (const item of parsed) {
      if (!item || typeof item !== 'object') continue
      if (typeof item.id !== 'string' || typeof item.name !== 'string') continue
      if (typeof item.scopePlatform !== 'string' || typeof item.scopeType !== 'string') continue
      if (typeof item.updatedAt !== 'number' || !Number.isFinite(item.updatedAt)) continue

      const ownerUserId =
        typeof item.ownerUserId === 'number' && Number.isFinite(item.ownerUserId)
          ? Math.floor(item.ownerUserId)
          : null

      records.push({
        id: item.id,
        name: item.name,
        scopePlatform: item.scopePlatform,
        scopeType: item.scopeType,
        shareScope: normalizeBulkEditTemplateShareScope(item.shareScope),
        groupIds: normalizeBulkEditTemplateGroupIDs(item.groupIds),
        state: (item.state as TState) ?? ({} as TState),
        updatedAt: item.updatedAt,
        ownerUserId
      })
    }
    return records
  } catch {
    return []
  }
}

export const serializeBulkEditTemplateRecords = <TState = Record<string, unknown>>(
  templates: BulkEditTemplateRecord<TState>[]
): string => JSON.stringify(templates)

export const upsertBulkEditTemplateRecord = <TState = Record<string, unknown>>(
  templates: BulkEditTemplateRecord<TState>[],
  template: BulkEditTemplateRecord<TState>
): BulkEditTemplateRecord<TState>[] => {
  const next = [...templates]
  const existingIdx = next.findIndex(
    (item) =>
      item.scopePlatform === template.scopePlatform &&
      item.scopeType === template.scopeType &&
      item.name.trim().toLowerCase() === template.name.trim().toLowerCase()
  )
  if (existingIdx >= 0) {
    next[existingIdx] = template
  } else {
    next.push(template)
  }
  return next
}

export const removeBulkEditTemplateRecord = <TState = Record<string, unknown>>(
  templates: BulkEditTemplateRecord<TState>[],
  templateID: string
): BulkEditTemplateRecord<TState>[] => templates.filter((item) => item.id !== templateID)

export const filterBulkEditTemplateRecordsByScope = <TState = Record<string, unknown>>(
  templates: BulkEditTemplateRecord<TState>[],
  scopePlatform?: AccountPlatform | '' | null,
  scopeType?: AccountType | '' | null
): BulkEditTemplateRecord<TState>[] => {
  if (!scopePlatform || !scopeType) return []
  return templates
    .filter(
      (item) => item.scopePlatform === scopePlatform && item.scopeType === scopeType
    )
    .sort((a, b) => b.updatedAt - a.updatedAt)
}
