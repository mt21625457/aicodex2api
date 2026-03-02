import { describe, expect, it } from 'vitest'
import {
  filterBulkEditTemplateRecordsByScope,
  normalizeBulkEditTemplateGroupIDs,
  normalizeBulkEditTemplateShareScope,
  parseBulkEditTemplateRecords,
  removeBulkEditTemplateRecord,
  serializeBulkEditTemplateRecords,
  upsertBulkEditTemplateRecord
} from '../bulkEditTemplateStore'

const templates = [
  {
    id: 'a',
    name: 'OpenAI OAuth Default',
    scopePlatform: 'openai',
    scopeType: 'oauth',
    shareScope: 'private',
    groupIds: [],
    state: { foo: 1 },
    updatedAt: 10
  },
  {
    id: 'b',
    name: 'OpenAI OAuth Latest',
    scopePlatform: 'openai',
    scopeType: 'oauth',
    shareScope: 'team',
    groupIds: [],
    state: { foo: 2 },
    updatedAt: 20
  },
  {
    id: 'c',
    name: 'OpenAI APIKey',
    scopePlatform: 'openai',
    scopeType: 'apikey',
    shareScope: 'groups',
    groupIds: [9],
    state: { foo: 3 },
    updatedAt: 5
  }
]

describe('bulkEditTemplateStore', () => {
  it('parses valid templates and ignores invalid payload', () => {
    const parsed = parseBulkEditTemplateRecords(JSON.stringify(templates))
    expect(parsed).toHaveLength(3)
    expect(parsed[0].shareScope).toBe('private')
    expect(parsed[2].groupIds).toEqual([9])
    expect(parseBulkEditTemplateRecords('')).toEqual([])
    expect(parseBulkEditTemplateRecords('invalid-json')).toEqual([])
    expect(parseBulkEditTemplateRecords(JSON.stringify({ foo: 'bar' }))).toEqual([])
  })

  it('normalizes legacy payload without share metadata', () => {
    const parsed = parseBulkEditTemplateRecords(
      JSON.stringify([
        {
          id: 'legacy',
          name: 'Legacy',
          scopePlatform: 'openai',
          scopeType: 'oauth',
          state: { foo: 1 },
          updatedAt: 1
        }
      ])
    )
    expect(parsed).toHaveLength(1)
    expect(parsed[0].shareScope).toBe('private')
    expect(parsed[0].groupIds).toEqual([])
  })

  it('serializes templates', () => {
    const raw = serializeBulkEditTemplateRecords(templates)
    expect(typeof raw).toBe('string')
    expect(parseBulkEditTemplateRecords(raw)).toHaveLength(3)
  })

  it('upserts by same scope + same name (case-insensitive)', () => {
    const next = upsertBulkEditTemplateRecord(templates, {
      id: 'd',
      name: 'openai oauth default',
      scopePlatform: 'openai',
      scopeType: 'oauth',
      shareScope: 'private',
      groupIds: [],
      state: { foo: 9 },
      updatedAt: 99
    })
    expect(next).toHaveLength(3)
    expect(next.find((item) => item.id === 'd')).toBeTruthy()
    expect(next.find((item) => item.id === 'a')).toBeFalsy()
  })

  it('removes template by id', () => {
    const next = removeBulkEditTemplateRecord(templates, 'b')
    expect(next).toHaveLength(2)
    expect(next.find((item) => item.id === 'b')).toBeFalsy()
  })

  it('filters and sorts by scope', () => {
    const scoped = filterBulkEditTemplateRecordsByScope(templates, 'openai', 'oauth')
    expect(scoped.map((item) => item.id)).toEqual(['b', 'a'])
    expect(filterBulkEditTemplateRecordsByScope(templates, '', 'oauth')).toEqual([])
    expect(filterBulkEditTemplateRecordsByScope(templates, 'openai', '')).toEqual([])
  })

  it('normalizes share scope and group ids', () => {
    expect(normalizeBulkEditTemplateShareScope('team')).toBe('team')
    expect(normalizeBulkEditTemplateShareScope('groups')).toBe('groups')
    expect(normalizeBulkEditTemplateShareScope('invalid')).toBe('private')
    expect(normalizeBulkEditTemplateGroupIDs([3, 1, 3, 2.8, -1] as any)).toEqual([1, 2, 3])
  })
})
