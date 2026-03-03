import { describe, expect, it } from 'vitest'
import {
  mapBulkEditTemplateFromRemote,
  mapBulkEditTemplateToUpsertRequest
} from '../bulkEditTemplateRemoteMapper'

describe('bulkEditTemplateRemoteMapper', () => {
  it('maps remote template to local template record', () => {
    const local = mapBulkEditTemplateFromRemote({
      id: 'tpl-1',
      name: 'OpenAI OAuth Shared',
      scope_platform: 'openai',
      scope_type: 'oauth',
      share_scope: 'groups',
      group_ids: [9, 3, 3],
      state: { enableBaseUrl: true },
      created_by: 8,
      updated_by: 8,
      created_at: 100,
      updated_at: 200
    })

    expect(local).toEqual({
      id: 'tpl-1',
      name: 'OpenAI OAuth Shared',
      scopePlatform: 'openai',
      scopeType: 'oauth',
      shareScope: 'groups',
      groupIds: [3, 9],
      state: { enableBaseUrl: true },
      updatedAt: 200,
      ownerUserId: 8
    })
  })

  it('normalizes malformed remote payload', () => {
    const local = mapBulkEditTemplateFromRemote({
      id: 'tpl-2',
      name: 'Broken',
      scope_platform: 'openai',
      scope_type: 'oauth',
      share_scope: 'bad' as any,
      group_ids: [0, 2, 2, 1],
      state: undefined as any,
      created_by: 0,
      updated_by: 0,
      created_at: 0,
      updated_at: 0
    })

    expect(local.shareScope).toBe('private')
    expect(local.groupIds).toEqual([1, 2])
    expect(local.ownerUserId).toBeNull()
    expect(typeof local.updatedAt).toBe('number')
    expect(local.updatedAt).toBeGreaterThan(0)
  })

  it('maps local model to upsert request', () => {
    const request = mapBulkEditTemplateToUpsertRequest({
      id: 'tpl-3',
      name: 'Team Template',
      scopePlatform: 'openai',
      scopeType: 'apikey',
      shareScope: 'team',
      groupIds: [8, 2, 8],
      state: { enableGroups: true }
    })

    expect(request).toEqual({
      id: 'tpl-3',
      name: 'Team Template',
      scope_platform: 'openai',
      scope_type: 'apikey',
      share_scope: 'team',
      group_ids: [2, 8],
      state: { enableGroups: true }
    })
  })
})
