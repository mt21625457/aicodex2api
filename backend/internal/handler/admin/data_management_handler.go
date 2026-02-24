package admin

import (
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

type DataManagementHandler struct {
	dataManagementService *service.DataManagementService
}

func NewDataManagementHandler(dataManagementService *service.DataManagementService) *DataManagementHandler {
	return &DataManagementHandler{dataManagementService: dataManagementService}
}

type TestS3ConnectionRequest struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region" binding:"required"`
	Bucket          string `json:"bucket" binding:"required"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Prefix          string `json:"prefix"`
	ForcePathStyle  bool   `json:"force_path_style"`
	UseSSL          bool   `json:"use_ssl"`
}

type CreateBackupJobRequest struct {
	BackupType     string `json:"backup_type" binding:"required,oneof=postgres redis full"`
	UploadToS3     bool   `json:"upload_to_s3"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (h *DataManagementHandler) GetAgentHealth(c *gin.Context) {
	health := h.getAgentHealth(c)
	payload := gin.H{
		"enabled":     health.Enabled,
		"reason":      health.Reason,
		"socket_path": health.SocketPath,
	}
	if health.Agent != nil {
		payload["agent"] = gin.H{
			"status":         health.Agent.Status,
			"version":        health.Agent.Version,
			"uptime_seconds": health.Agent.UptimeSeconds,
		}
	}
	response.Success(c, payload)
}

func (h *DataManagementHandler) GetConfig(c *gin.Context) {
	if !h.requireAgentEnabled(c) {
		return
	}
	cfg, err := h.dataManagementService.GetConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

func (h *DataManagementHandler) UpdateConfig(c *gin.Context) {
	var req service.DataManagementConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	if !h.requireAgentEnabled(c) {
		return
	}
	cfg, err := h.dataManagementService.UpdateConfig(c.Request.Context(), req)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

func (h *DataManagementHandler) TestS3(c *gin.Context) {
	var req TestS3ConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	if !h.requireAgentEnabled(c) {
		return
	}
	result, err := h.dataManagementService.ValidateS3(c.Request.Context(), service.DataManagementS3Config{
		Enabled:         true,
		Endpoint:        req.Endpoint,
		Region:          req.Region,
		Bucket:          req.Bucket,
		AccessKeyID:     req.AccessKeyID,
		SecretAccessKey: req.SecretAccessKey,
		Prefix:          req.Prefix,
		ForcePathStyle:  req.ForcePathStyle,
		UseSSL:          req.UseSSL,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"ok": result.OK, "message": result.Message})
}

func (h *DataManagementHandler) CreateBackupJob(c *gin.Context) {
	var req CreateBackupJobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	req.IdempotencyKey = normalizeBackupIdempotencyKey(c.GetHeader("X-Idempotency-Key"), req.IdempotencyKey)
	if !h.requireAgentEnabled(c) {
		return
	}

	triggeredBy := "admin:unknown"
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok {
		triggeredBy = "admin:" + strconv.FormatInt(subject.UserID, 10)
	}
	job, err := h.dataManagementService.CreateBackupJob(c.Request.Context(), service.DataManagementCreateBackupJobInput{
		BackupType:     req.BackupType,
		UploadToS3:     req.UploadToS3,
		TriggeredBy:    triggeredBy,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"job_id": job.JobID, "status": job.Status})
}

func (h *DataManagementHandler) ListBackupJobs(c *gin.Context) {
	if !h.requireAgentEnabled(c) {
		return
	}

	pageSize := int32(20)
	if raw := strings.TrimSpace(c.Query("page_size")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			response.BadRequest(c, "Invalid page_size")
			return
		}
		pageSize = int32(v)
	}

	result, err := h.dataManagementService.ListBackupJobs(c.Request.Context(), service.DataManagementListBackupJobsInput{
		PageSize:   pageSize,
		PageToken:  c.Query("page_token"),
		Status:     c.Query("status"),
		BackupType: c.Query("backup_type"),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *DataManagementHandler) GetBackupJob(c *gin.Context) {
	jobID := strings.TrimSpace(c.Param("job_id"))
	if jobID == "" {
		response.BadRequest(c, "Invalid backup job ID")
		return
	}

	if !h.requireAgentEnabled(c) {
		return
	}
	job, err := h.dataManagementService.GetBackupJob(c.Request.Context(), jobID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, job)
}

func (h *DataManagementHandler) requireAgentEnabled(c *gin.Context) bool {
	if h.dataManagementService == nil {
		err := infraerrors.ServiceUnavailable(
			service.BackupAgentUnavailableReason,
			"backup agent service is not configured",
		).WithMetadata(map[string]string{"socket_path": service.DefaultBackupAgentSocketPath})
		response.ErrorFrom(c, err)
		return false
	}

	if err := h.dataManagementService.EnsureAgentEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return false
	}

	return true
}

func (h *DataManagementHandler) getAgentHealth(c *gin.Context) service.DataManagementAgentHealth {
	if h.dataManagementService == nil {
		return service.DataManagementAgentHealth{
			Enabled:    false,
			Reason:     service.BackupAgentUnavailableReason,
			SocketPath: service.DefaultBackupAgentSocketPath,
		}
	}
	return h.dataManagementService.GetAgentHealth(c.Request.Context())
}

func normalizeBackupIdempotencyKey(headerValue, bodyValue string) string {
	headerKey := strings.TrimSpace(headerValue)
	if headerKey != "" {
		return headerKey
	}
	return strings.TrimSpace(bodyValue)
}
