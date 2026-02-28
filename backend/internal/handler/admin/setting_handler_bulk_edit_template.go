package admin

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type UpsertBulkEditTemplateRequest struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	ScopePlatform string         `json:"scope_platform"`
	ScopeType     string         `json:"scope_type"`
	ShareScope    string         `json:"share_scope"`
	GroupIDs      []int64        `json:"group_ids"`
	State         map[string]any `json:"state"`
}

type RollbackBulkEditTemplateRequest struct {
	VersionID string `json:"version_id"`
}

// ListBulkEditTemplates 获取批量编辑模板列表
// GET /api/v1/admin/settings/bulk-edit-templates
func (h *SettingHandler) ListBulkEditTemplates(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Unauthorized")
		return
	}

	scopeGroupIDs, err := parseScopeGroupIDs(c.Query("scope_group_ids"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	items, listErr := h.settingService.ListBulkEditTemplates(c.Request.Context(), service.BulkEditTemplateQuery{
		ScopePlatform:   c.Query("scope_platform"),
		ScopeType:       c.Query("scope_type"),
		ScopeGroupIDs:   scopeGroupIDs,
		RequesterUserID: subject.UserID,
	})
	if listErr != nil {
		response.ErrorFrom(c, listErr)
		return
	}

	response.Success(c, gin.H{"items": items})
}

// UpsertBulkEditTemplate 创建/更新批量编辑模板
// POST /api/v1/admin/settings/bulk-edit-templates
func (h *SettingHandler) UpsertBulkEditTemplate(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Unauthorized")
		return
	}

	var req UpsertBulkEditTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	item, upsertErr := h.settingService.UpsertBulkEditTemplate(
		c.Request.Context(),
		service.BulkEditTemplateUpsertInput{
			ID:              req.ID,
			Name:            req.Name,
			ScopePlatform:   req.ScopePlatform,
			ScopeType:       req.ScopeType,
			ShareScope:      req.ShareScope,
			GroupIDs:        req.GroupIDs,
			State:           req.State,
			RequesterUserID: subject.UserID,
		},
	)
	if upsertErr != nil {
		response.ErrorFrom(c, upsertErr)
		return
	}

	response.Success(c, item)
}

// DeleteBulkEditTemplate 删除批量编辑模板
// DELETE /api/v1/admin/settings/bulk-edit-templates/:template_id
func (h *SettingHandler) DeleteBulkEditTemplate(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Unauthorized")
		return
	}

	templateID := strings.TrimSpace(c.Param("template_id"))
	if templateID == "" {
		response.BadRequest(c, "template_id is required")
		return
	}

	if err := h.settingService.DeleteBulkEditTemplate(c.Request.Context(), templateID, subject.UserID); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"deleted": true})
}

// ListBulkEditTemplateVersions 获取模板版本历史
// GET /api/v1/admin/settings/bulk-edit-templates/:template_id/versions
func (h *SettingHandler) ListBulkEditTemplateVersions(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Unauthorized")
		return
	}

	templateID := strings.TrimSpace(c.Param("template_id"))
	if templateID == "" {
		response.BadRequest(c, "template_id is required")
		return
	}

	scopeGroupIDs, err := parseScopeGroupIDs(c.Query("scope_group_ids"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	items, listErr := h.settingService.ListBulkEditTemplateVersions(
		c.Request.Context(),
		service.BulkEditTemplateVersionQuery{
			TemplateID:      templateID,
			ScopeGroupIDs:   scopeGroupIDs,
			RequesterUserID: subject.UserID,
		},
	)
	if listErr != nil {
		response.ErrorFrom(c, listErr)
		return
	}

	response.Success(c, gin.H{"items": items})
}

// RollbackBulkEditTemplate 回滚模板到指定版本
// POST /api/v1/admin/settings/bulk-edit-templates/:template_id/rollback
func (h *SettingHandler) RollbackBulkEditTemplate(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Unauthorized")
		return
	}

	templateID := strings.TrimSpace(c.Param("template_id"))
	if templateID == "" {
		response.BadRequest(c, "template_id is required")
		return
	}

	scopeGroupIDs, err := parseScopeGroupIDs(c.Query("scope_group_ids"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	var req RollbackBulkEditTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	item, rollbackErr := h.settingService.RollbackBulkEditTemplate(
		c.Request.Context(),
		service.BulkEditTemplateRollbackInput{
			TemplateID:      templateID,
			VersionID:       req.VersionID,
			ScopeGroupIDs:   scopeGroupIDs,
			RequesterUserID: subject.UserID,
		},
	)
	if rollbackErr != nil {
		response.ErrorFrom(c, rollbackErr)
		return
	}

	response.Success(c, item)
}

func parseScopeGroupIDs(raw string) ([]int64, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}

	parts := strings.Split(trimmed, ",")
	if len(parts) == 0 {
		return nil, nil
	}

	seen := make(map[int64]struct{}, len(parts))
	groupIDs := make([]int64, 0, len(parts))
	for _, part := range parts {
		candidate := strings.TrimSpace(part)
		if candidate == "" {
			continue
		}

		groupID, err := strconv.ParseInt(candidate, 10, 64)
		if err != nil || groupID <= 0 {
			return nil, fmt.Errorf("scope_group_ids must be comma-separated positive integers")
		}
		if _, exists := seen[groupID]; exists {
			continue
		}
		seen[groupID] = struct{}{}
		groupIDs = append(groupIDs, groupID)
	}

	return groupIDs, nil
}
