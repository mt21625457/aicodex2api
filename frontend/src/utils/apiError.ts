type UnknownRecord = Record<string, unknown>

function asRecord(value: unknown): UnknownRecord | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return null
  }
  return value as UnknownRecord
}

function getResponseRecord(error: unknown): UnknownRecord | null {
  const errorRecord = asRecord(error)
  if (!errorRecord) {
    return null
  }
  return asRecord(errorRecord.response)
}

function getErrorDataRecord(error: unknown): UnknownRecord | null {
  const errorRecord = asRecord(error)
  if (!errorRecord) {
    return null
  }

  // New apiClient shape: Promise.reject({ status, code, message, ... })
  const topLevelData = asRecord(errorRecord.data)
  if (topLevelData) {
    return topLevelData
  }

  // Legacy axios-like shape: { response: { data: ... } }
  const responseRecord = getResponseRecord(error)
  if (!responseRecord) {
    return null
  }
  return asRecord(responseRecord.data)
}

function getStringField(record: UnknownRecord | null, key: string): string {
  if (!record) {
    return ''
  }
  const value = record[key]
  if (typeof value !== 'string') {
    return ''
  }
  return value.trim()
}

export function getAPIErrorStatus(error: unknown): number | null {
  const errorRecord = asRecord(error)
  if (!errorRecord) {
    return null
  }

  const directStatus = errorRecord.status
  if (typeof directStatus === 'number') {
    return directStatus
  }

  const responseRecord = getResponseRecord(error)
  if (!responseRecord) {
    return null
  }
  const responseStatus = responseRecord.status
  return typeof responseStatus === 'number' ? responseStatus : null
}

export function getAPIErrorMessage(error: unknown, fallback: string): string {
  const errorRecord = asRecord(error)
  const dataRecord = getErrorDataRecord(error)
  const responseRecord = getResponseRecord(error)

  const message =
    getStringField(errorRecord, 'message') ||
    getStringField(dataRecord, 'message') ||
    getStringField(dataRecord, 'detail') ||
    getStringField(responseRecord, 'message')

  if (message) {
    return message
  }

  if (error instanceof Error && error.message.trim()) {
    return error.message.trim()
  }

  return fallback
}

export function getAPIErrorField(error: unknown, field: string): unknown {
  const errorRecord = asRecord(error)
  if (errorRecord && errorRecord[field] !== undefined) {
    return errorRecord[field]
  }

  const dataRecord = getErrorDataRecord(error)
  if (dataRecord && dataRecord[field] !== undefined) {
    return dataRecord[field]
  }

  return undefined
}

export function isMixedChannelWarningError(error: unknown): boolean {
  if (getAPIErrorStatus(error) !== 409) {
    return false
  }

  const warningToken = 'mixed_channel_warning'
  const warningField = getAPIErrorField(error, 'error')
  if (typeof warningField === 'string' && warningField === warningToken) {
    return true
  }

  const warningCode = getAPIErrorField(error, 'code')
  if (typeof warningCode === 'string' && warningCode === warningToken) {
    return true
  }

  const msg = getAPIErrorMessage(error, '')
  return msg.includes(warningToken)
}
