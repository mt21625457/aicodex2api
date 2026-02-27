/**
 * Sora 客户端 API
 * 封装所有 Sora 生成、作品库、配额等接口调用
 */

import { apiClient } from './client'

// ==================== 类型定义 ====================

export interface SoraGeneration {
  id: number
  user_id: number
  model: string
  prompt: string
  media_type: string
  status: string // pending | generating | completed | failed | cancelled
  storage_type: string // upstream | s3 | local
  media_url: string
  media_urls: string[]
  s3_object_keys: string[]
  file_size_bytes: number
  error_message: string
  created_at: string
  completed_at?: string
}

export interface GenerateRequest {
  model: string
  prompt: string
  media_type?: string
  image_input?: string
}

export interface GenerateResponse {
  generation_id: number
  status: string
}

export interface GenerationListResponse {
  data: SoraGeneration[]
  total: number
  page: number
}

export interface QuotaInfo {
  quota_bytes: number
  used_bytes: number
  available_bytes: number
  quota_source: string // user | group | system | unlimited
  source?: string // 兼容旧字段
}

export interface StorageStatus {
  s3_enabled: boolean
  s3_healthy: boolean
  local_enabled: boolean
}

export interface SoraModel {
  id: string
  name: string
  type: string // video | image
  orientation?: string
  duration?: number
}

// ==================== API 方法 ====================

/** 异步生成 — 创建 pending 记录后立即返回 */
export async function generate(req: GenerateRequest): Promise<GenerateResponse> {
  const { data } = await apiClient.post<GenerateResponse>('/sora/generate', req)
  return data
}

/** 查询生成记录列表 */
export async function listGenerations(params?: {
  page?: number
  page_size?: number
  status?: string
  storage_type?: string
  media_type?: string
}): Promise<GenerationListResponse> {
  const { data } = await apiClient.get<GenerationListResponse>('/sora/generations', { params })
  return data
}

/** 查询生成记录详情 */
export async function getGeneration(id: number): Promise<SoraGeneration> {
  const { data } = await apiClient.get<SoraGeneration>(`/sora/generations/${id}`)
  return data
}

/** 删除生成记录 */
export async function deleteGeneration(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>(`/sora/generations/${id}`)
  return data
}

/** 取消生成任务 */
export async function cancelGeneration(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.post<{ message: string }>(`/sora/generations/${id}/cancel`)
  return data
}

/** 手动保存到 S3 */
export async function saveToStorage(
  id: number
): Promise<{ message: string; object_key: string; object_keys?: string[] }> {
  const { data } = await apiClient.post<{ message: string; object_key: string; object_keys?: string[] }>(
    `/sora/generations/${id}/save`
  )
  return data
}

/** 查询配额信息 */
export async function getQuota(): Promise<QuotaInfo> {
  const { data } = await apiClient.get<QuotaInfo>('/sora/quota')
  return data
}

/** 获取可用模型列表 */
export async function getModels(): Promise<SoraModel[]> {
  const { data } = await apiClient.get<SoraModel[]>('/sora/models')
  return data
}

/** 获取存储状态 */
export async function getStorageStatus(): Promise<StorageStatus> {
  const { data } = await apiClient.get<StorageStatus>('/sora/storage-status')
  return data
}

const soraAPI = {
  generate,
  listGenerations,
  getGeneration,
  deleteGeneration,
  cancelGeneration,
  saveToStorage,
  getQuota,
  getModels,
  getStorageStatus
}

export default soraAPI
