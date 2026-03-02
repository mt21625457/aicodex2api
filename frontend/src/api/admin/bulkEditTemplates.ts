import { apiClient } from '../client'
import type { AccountPlatform, AccountType } from '@/types'

export type BulkEditTemplateShareScope = 'private' | 'team' | 'groups'

export interface BulkEditTemplateRecord<TState = Record<string, unknown>> {
  id: string
  name: string
  scope_platform: AccountPlatform | ''
  scope_type: AccountType | ''
  share_scope: BulkEditTemplateShareScope
  group_ids: number[]
  state: TState
  created_by: number
  updated_by: number
  created_at: number
  updated_at: number
}

export interface BulkEditTemplateVersionRecord<TState = Record<string, unknown>> {
  version_id: string
  share_scope: BulkEditTemplateShareScope
  group_ids: number[]
  state: TState
  updated_by: number
  updated_at: number
}

export interface GetBulkEditTemplatesParams {
  scope_platform?: AccountPlatform | ''
  scope_type?: AccountType | ''
  scope_group_ids?: number[]
}

export interface GetBulkEditTemplateVersionsParams {
  scope_group_ids?: number[]
}

export interface UpsertBulkEditTemplateRequest<TState = Record<string, unknown>> {
  id?: string
  name: string
  scope_platform: AccountPlatform | ''
  scope_type: AccountType | ''
  share_scope: BulkEditTemplateShareScope
  group_ids: number[]
  state: TState
}

export interface RollbackBulkEditTemplateRequest {
  version_id: string
}

export async function getBulkEditTemplates<TState = Record<string, unknown>>(
  params: GetBulkEditTemplatesParams
): Promise<BulkEditTemplateRecord<TState>[]> {
  const query: Record<string, unknown> = {}
  if (params.scope_platform) query.scope_platform = params.scope_platform
  if (params.scope_type) query.scope_type = params.scope_type
  if (Array.isArray(params.scope_group_ids) && params.scope_group_ids.length > 0) {
    query.scope_group_ids = params.scope_group_ids.join(',')
  }

  const { data } = await apiClient.get<{ items: BulkEditTemplateRecord<TState>[] }>(
    '/admin/settings/bulk-edit-templates',
    { params: query }
  )
  return Array.isArray(data.items) ? data.items : []
}

export async function getBulkEditTemplateVersions<TState = Record<string, unknown>>(
  templateID: string,
  params: GetBulkEditTemplateVersionsParams = {}
): Promise<BulkEditTemplateVersionRecord<TState>[]> {
  const query: Record<string, unknown> = {}
  if (Array.isArray(params.scope_group_ids) && params.scope_group_ids.length > 0) {
    query.scope_group_ids = params.scope_group_ids.join(',')
  }

  const { data } = await apiClient.get<{ items: BulkEditTemplateVersionRecord<TState>[] }>(
    `/admin/settings/bulk-edit-templates/${templateID}/versions`,
    { params: query }
  )
  return Array.isArray(data.items) ? data.items : []
}

export async function upsertBulkEditTemplate<TState = Record<string, unknown>>(
  request: UpsertBulkEditTemplateRequest<TState>
): Promise<BulkEditTemplateRecord<TState>> {
  const { data } = await apiClient.post<BulkEditTemplateRecord<TState>>(
    '/admin/settings/bulk-edit-templates',
    request
  )
  return data
}

export async function rollbackBulkEditTemplate<TState = Record<string, unknown>>(
  templateID: string,
  request: RollbackBulkEditTemplateRequest,
  params: GetBulkEditTemplateVersionsParams = {}
): Promise<BulkEditTemplateRecord<TState>> {
  const query: Record<string, unknown> = {}
  if (Array.isArray(params.scope_group_ids) && params.scope_group_ids.length > 0) {
    query.scope_group_ids = params.scope_group_ids.join(',')
  }

  const { data } = await apiClient.post<BulkEditTemplateRecord<TState>>(
    `/admin/settings/bulk-edit-templates/${templateID}/rollback`,
    request,
    { params: query }
  )
  return data
}

export async function deleteBulkEditTemplate(templateID: string): Promise<{ deleted: boolean }> {
  const { data } = await apiClient.delete<{ deleted: boolean }>(
    `/admin/settings/bulk-edit-templates/${templateID}`
  )
  return data
}

const bulkEditTemplatesAPI = {
  getBulkEditTemplates,
  getBulkEditTemplateVersions,
  upsertBulkEditTemplate,
  rollbackBulkEditTemplate,
  deleteBulkEditTemplate
}

export default bulkEditTemplatesAPI
