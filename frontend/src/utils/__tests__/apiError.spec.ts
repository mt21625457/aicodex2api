import { describe, expect, it } from 'vitest'

import {
  getAPIErrorField,
  getAPIErrorMessage,
  getAPIErrorStatus,
  isMixedChannelWarningError
} from '../apiError'

describe('apiError helpers', () => {
  it('extracts status/message from normalized apiClient error', () => {
    const err = {
      status: 409,
      message: "mixed_channel_warning: Group 'g' contains both Anthropic and Antigravity"
    }

    expect(getAPIErrorStatus(err)).toBe(409)
    expect(getAPIErrorMessage(err, 'fallback')).toContain('mixed_channel_warning')
    expect(isMixedChannelWarningError(err)).toBe(true)
  })

  it('extracts message and custom field from axios-like response payload', () => {
    const err = {
      response: {
        status: 409,
        data: {
          error: 'mixed_channel_warning',
          message: 'mixed_channel_warning: detected'
        }
      }
    }

    expect(getAPIErrorStatus(err)).toBe(409)
    expect(getAPIErrorField(err, 'error')).toBe('mixed_channel_warning')
    expect(getAPIErrorMessage(err, 'fallback')).toBe('mixed_channel_warning: detected')
    expect(isMixedChannelWarningError(err)).toBe(true)
  })

  it('falls back when no structured message exists', () => {
    expect(getAPIErrorStatus(null)).toBeNull()
    expect(getAPIErrorMessage(null, 'fallback')).toBe('fallback')
    expect(isMixedChannelWarningError(null)).toBe(false)
  })

  it('supports axios-like response.status and detail fallback', () => {
    const err = {
      response: {
        status: 400,
        data: {
          detail: 'invalid payload'
        }
      }
    }

    expect(getAPIErrorStatus(err)).toBe(400)
    expect(getAPIErrorMessage(err, 'fallback')).toBe('invalid payload')
    expect(isMixedChannelWarningError(err)).toBe(false)
  })

  it('supports top-level data payload from normalized errors', () => {
    const err = {
      status: 409,
      data: {
        error: 'mixed_channel_warning',
        message: 'mixed_channel_warning from top-level data'
      }
    }

    expect(getAPIErrorField(err, 'error')).toBe('mixed_channel_warning')
    expect(getAPIErrorMessage(err, 'fallback')).toBe('mixed_channel_warning from top-level data')
    expect(isMixedChannelWarningError(err)).toBe(true)
  })

  it('supports warning code field', () => {
    const err = {
      status: 409,
      code: 'mixed_channel_warning',
      message: 'conflict'
    }

    expect(isMixedChannelWarningError(err)).toBe(true)
  })

  it('does not treat non-409 as mixed channel warning', () => {
    const err = {
      status: 400,
      error: 'mixed_channel_warning',
      message: 'mixed_channel_warning but wrong status'
    }

    expect(isMixedChannelWarningError(err)).toBe(false)
  })

  it('falls back to Error.message', () => {
    const err = new Error('network timeout')
    expect(getAPIErrorMessage(err, 'fallback')).toBe('network timeout')
  })
})
