import { apiClient } from '../client'

export type BackupType = 'postgres' | 'redis' | 'full'
export type BackupJobStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'partial_succeeded'

export interface BackupAgentInfo {
  status: string
  version: string
  uptime_seconds: number
}

export interface BackupAgentHealth {
  enabled: boolean
  reason: string
  socket_path: string
  agent?: BackupAgentInfo
}

export interface DataManagementPostgresConfig {
  host: string
  port: number
  user: string
  password?: string
  password_configured?: boolean
  database: string
  ssl_mode: string
  container_name: string
}

export interface DataManagementRedisConfig {
  addr: string
  username: string
  password?: string
  password_configured?: boolean
  db: number
  container_name: string
}

export interface DataManagementS3Config {
  enabled: boolean
  endpoint: string
  region: string
  bucket: string
  access_key_id: string
  secret_access_key?: string
  secret_access_key_configured?: boolean
  prefix: string
  force_path_style: boolean
  use_ssl: boolean
}

export interface DataManagementConfig {
  source_mode: 'direct' | 'docker_exec'
  backup_root: string
  sqlite_path?: string
  retention_days: number
  keep_last: number
  postgres: DataManagementPostgresConfig
  redis: DataManagementRedisConfig
  s3: DataManagementS3Config
}

export interface TestS3Request {
  endpoint: string
  region: string
  bucket: string
  access_key_id: string
  secret_access_key: string
  prefix?: string
  force_path_style?: boolean
  use_ssl?: boolean
}

export interface TestS3Response {
  ok: boolean
  message: string
}

export interface CreateBackupJobRequest {
  backup_type: BackupType
  upload_to_s3?: boolean
  idempotency_key?: string
}

export interface CreateBackupJobResponse {
  job_id: string
  status: BackupJobStatus
}

export interface BackupArtifactInfo {
  local_path: string
  size_bytes: number
  sha256: string
}

export interface BackupS3Info {
  bucket: string
  key: string
  etag: string
}

export interface BackupJob {
  job_id: string
  backup_type: BackupType
  status: BackupJobStatus
  triggered_by: string
  started_at?: string
  finished_at?: string
  error_message?: string
  artifact?: BackupArtifactInfo
  s3?: BackupS3Info
}

export interface ListBackupJobsRequest {
  page_size?: number
  page_token?: string
  status?: BackupJobStatus
  backup_type?: BackupType
}

export interface ListBackupJobsResponse {
  items: BackupJob[]
  next_page_token?: string
}

export async function getAgentHealth(): Promise<BackupAgentHealth> {
  const { data } = await apiClient.get<BackupAgentHealth>('/admin/data-management/agent/health')
  return data
}

export async function getConfig(): Promise<DataManagementConfig> {
  const { data } = await apiClient.get<DataManagementConfig>('/admin/data-management/config')
  return data
}

export async function updateConfig(request: DataManagementConfig): Promise<DataManagementConfig> {
  const { data } = await apiClient.put<DataManagementConfig>('/admin/data-management/config', request)
  return data
}

export async function testS3(request: TestS3Request): Promise<TestS3Response> {
  const { data } = await apiClient.post<TestS3Response>('/admin/data-management/s3/test', request)
  return data
}

export async function createBackupJob(request: CreateBackupJobRequest): Promise<CreateBackupJobResponse> {
  const headers = request.idempotency_key
    ? { 'X-Idempotency-Key': request.idempotency_key }
    : undefined

  const { data } = await apiClient.post<CreateBackupJobResponse>(
    '/admin/data-management/backups',
    request,
    { headers }
  )
  return data
}

export async function listBackupJobs(request?: ListBackupJobsRequest): Promise<ListBackupJobsResponse> {
  const { data } = await apiClient.get<ListBackupJobsResponse>('/admin/data-management/backups', {
    params: request
  })
  return data
}

export async function getBackupJob(jobID: string): Promise<BackupJob> {
  const { data } = await apiClient.get<BackupJob>(`/admin/data-management/backups/${jobID}`)
  return data
}

export const dataManagementAPI = {
  getAgentHealth,
  getConfig,
  updateConfig,
  testS3,
  createBackupJob,
  listBackupJobs,
  getBackupJob
}

export default dataManagementAPI
