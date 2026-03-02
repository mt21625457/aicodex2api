import type { AccountPlatform, AccountType } from '@/types'
import type {
  BulkEditTemplateRecord as BulkEditTemplateRemoteRecord,
  BulkEditTemplateShareScope,
  UpsertBulkEditTemplateRequest
} from '@/api/admin/bulkEditTemplates'
import {
  normalizeBulkEditTemplateGroupIDs,
  normalizeBulkEditTemplateShareScope,
  type BulkEditTemplateRecord
} from './bulkEditTemplateStore'

const normalizeTimestamp = (value: unknown): number =>
  typeof value === 'number' && Number.isFinite(value) && value > 0
    ? Math.floor(value)
    : Date.now()

const normalizeOwnerUserID = (value: unknown): number | null =>
  typeof value === 'number' && Number.isFinite(value) && value > 0 ? Math.floor(value) : null

export const mapBulkEditTemplateFromRemote = <TState = Record<string, unknown>>(
  record: BulkEditTemplateRemoteRecord<TState>
): BulkEditTemplateRecord<TState> => ({
  id: typeof record.id === 'string' ? record.id : '',
  name: typeof record.name === 'string' ? record.name : '',
  scopePlatform: (record.scope_platform ?? '') as AccountPlatform | '',
  scopeType: (record.scope_type ?? '') as AccountType | '',
  shareScope: normalizeBulkEditTemplateShareScope(record.share_scope),
  groupIds: normalizeBulkEditTemplateGroupIDs(record.group_ids),
  state: (record.state ?? ({} as TState)) as TState,
  updatedAt: normalizeTimestamp(record.updated_at),
  ownerUserId: normalizeOwnerUserID(record.created_by)
})

export interface BulkEditTemplateUpsertModel<TState = Record<string, unknown>> {
  id?: string
  name: string
  scopePlatform: AccountPlatform | ''
  scopeType: AccountType | ''
  shareScope: BulkEditTemplateShareScope
  groupIds: number[]
  state: TState
}

export const mapBulkEditTemplateToUpsertRequest = <TState = Record<string, unknown>>(
  model: BulkEditTemplateUpsertModel<TState>
): UpsertBulkEditTemplateRequest<TState> => ({
  ...(model.id ? { id: model.id } : {}),
  name: model.name,
  scope_platform: model.scopePlatform,
  scope_type: model.scopeType,
  share_scope: normalizeBulkEditTemplateShareScope(model.shareScope),
  group_ids: normalizeBulkEditTemplateGroupIDs(model.groupIds),
  state: model.state
})
