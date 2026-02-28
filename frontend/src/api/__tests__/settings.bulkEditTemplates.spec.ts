import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  deleteBulkEditTemplate,
  getBulkEditTemplates,
  getBulkEditTemplateVersions,
  rollbackBulkEditTemplate,
  upsertBulkEditTemplate
} from '../admin/bulkEditTemplates'
import { apiClient } from '../client'

vi.mock('../client', () => ({
  apiClient: {
    get: vi.fn(),
    post: vi.fn(),
    delete: vi.fn()
  }
}))

describe('admin settings bulk-edit templates api', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('requests template list with expected query params', async () => {
    ;(apiClient.get as any).mockResolvedValue({
      data: {
        items: [
          {
            id: 'tpl-1',
            name: 'Template',
            scope_platform: 'openai',
            scope_type: 'oauth',
            share_scope: 'team',
            group_ids: [],
            state: {},
            created_by: 1,
            updated_by: 1,
            created_at: 1,
            updated_at: 2
          }
        ]
      }
    })

    const items = await getBulkEditTemplates({
      scope_platform: 'openai',
      scope_type: 'oauth',
      scope_group_ids: [3, 9]
    })

    expect(apiClient.get).toHaveBeenCalledWith('/admin/settings/bulk-edit-templates', {
      params: {
        scope_platform: 'openai',
        scope_type: 'oauth',
        scope_group_ids: '3,9'
      }
    })
    expect(items).toHaveLength(1)
    expect(items[0].id).toBe('tpl-1')
  })

  it('returns empty list when response items is invalid', async () => {
    ;(apiClient.get as any).mockResolvedValue({ data: { items: null } })
    const items = await getBulkEditTemplates({})
    expect(items).toEqual([])
  })

  it('posts upsert payload and returns saved template', async () => {
    ;(apiClient.post as any).mockResolvedValue({
      data: {
        id: 'tpl-2',
        name: 'Shared',
        scope_platform: 'openai',
        scope_type: 'apikey',
        share_scope: 'groups',
        group_ids: [1],
        state: { enableProxy: true },
        created_by: 2,
        updated_by: 2,
        created_at: 10,
        updated_at: 11
      }
    })

    const saved = await upsertBulkEditTemplate({
      id: 'tpl-2',
      name: 'Shared',
      scope_platform: 'openai',
      scope_type: 'apikey',
      share_scope: 'groups',
      group_ids: [1],
      state: { enableProxy: true }
    })

    expect(apiClient.post).toHaveBeenCalledWith('/admin/settings/bulk-edit-templates', {
      id: 'tpl-2',
      name: 'Shared',
      scope_platform: 'openai',
      scope_type: 'apikey',
      share_scope: 'groups',
      group_ids: [1],
      state: { enableProxy: true }
    })
    expect(saved.id).toBe('tpl-2')
  })

  it('requests template versions with scope group params', async () => {
    ;(apiClient.get as any).mockResolvedValue({
      data: {
        items: [
          {
            version_id: 'ver-1',
            share_scope: 'team',
            group_ids: [],
            state: { enableOpenAIWSMode: true },
            updated_by: 11,
            updated_at: 100
          }
        ]
      }
    })

    const items = await getBulkEditTemplateVersions('tpl-2', { scope_group_ids: [5, 8] })

    expect(apiClient.get).toHaveBeenCalledWith('/admin/settings/bulk-edit-templates/tpl-2/versions', {
      params: {
        scope_group_ids: '5,8'
      }
    })
    expect(items).toHaveLength(1)
    expect(items[0].version_id).toBe('ver-1')
  })

  it('returns empty versions list when payload is invalid', async () => {
    ;(apiClient.get as any).mockResolvedValue({ data: { items: undefined } })

    const items = await getBulkEditTemplateVersions('tpl-any')

    expect(apiClient.get).toHaveBeenCalledWith('/admin/settings/bulk-edit-templates/tpl-any/versions', {
      params: {}
    })
    expect(items).toEqual([])
  })

  it('posts rollback request with optional query params', async () => {
    ;(apiClient.post as any).mockResolvedValue({
      data: {
        id: 'tpl-3',
        name: 'Rollbacked',
        scope_platform: 'openai',
        scope_type: 'oauth',
        share_scope: 'private',
        group_ids: [],
        state: { enableOpenAIPassthrough: false },
        created_by: 1,
        updated_by: 2,
        created_at: 10,
        updated_at: 12
      }
    })

    const saved = await rollbackBulkEditTemplate(
      'tpl-3',
      { version_id: 'ver-2' },
      { scope_group_ids: [2] }
    )

    expect(apiClient.post).toHaveBeenCalledWith(
      '/admin/settings/bulk-edit-templates/tpl-3/rollback',
      { version_id: 'ver-2' },
      { params: { scope_group_ids: '2' } }
    )
    expect(saved.id).toBe('tpl-3')
  })

  it('calls delete endpoint for template removal', async () => {
    ;(apiClient.delete as any).mockResolvedValue({ data: { deleted: true } })

    const result = await deleteBulkEditTemplate('tpl-9')

    expect(apiClient.delete).toHaveBeenCalledWith('/admin/settings/bulk-edit-templates/tpl-9')
    expect(result).toEqual({ deleted: true })
  })
})
