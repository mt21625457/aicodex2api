package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	BulkEditTemplateShareScopePrivate = "private"
	BulkEditTemplateShareScopeTeam    = "team"
	BulkEditTemplateShareScopeGroups  = "groups"
)

var (
	ErrBulkEditTemplateNotFound        = infraerrors.NotFound("BULK_EDIT_TEMPLATE_NOT_FOUND", "bulk edit template not found")
	ErrBulkEditTemplateVersionNotFound = infraerrors.NotFound(
		"BULK_EDIT_TEMPLATE_VERSION_NOT_FOUND",
		"bulk edit template version not found",
	)
	ErrBulkEditTemplateForbidden = infraerrors.Forbidden(
		"BULK_EDIT_TEMPLATE_FORBIDDEN",
		"no permission to modify this bulk edit template",
	)
	bulkEditTemplateRandRead = rand.Read
)

type BulkEditTemplate struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	ScopePlatform string         `json:"scope_platform"`
	ScopeType     string         `json:"scope_type"`
	ShareScope    string         `json:"share_scope"`
	GroupIDs      []int64        `json:"group_ids"`
	State         map[string]any `json:"state"`
	CreatedBy     int64          `json:"created_by"`
	UpdatedBy     int64          `json:"updated_by"`
	CreatedAt     int64          `json:"created_at"`
	UpdatedAt     int64          `json:"updated_at"`
}

type BulkEditTemplateQuery struct {
	ScopePlatform   string
	ScopeType       string
	ScopeGroupIDs   []int64
	RequesterUserID int64
}

type BulkEditTemplateVersion struct {
	VersionID  string         `json:"version_id"`
	ShareScope string         `json:"share_scope"`
	GroupIDs   []int64        `json:"group_ids"`
	State      map[string]any `json:"state"`
	UpdatedBy  int64          `json:"updated_by"`
	UpdatedAt  int64          `json:"updated_at"`
}

type BulkEditTemplateVersionQuery struct {
	TemplateID      string
	ScopeGroupIDs   []int64
	RequesterUserID int64
}

type BulkEditTemplateUpsertInput struct {
	ID              string
	Name            string
	ScopePlatform   string
	ScopeType       string
	ShareScope      string
	GroupIDs        []int64
	State           map[string]any
	RequesterUserID int64
}

type BulkEditTemplateRollbackInput struct {
	TemplateID      string
	VersionID       string
	ScopeGroupIDs   []int64
	RequesterUserID int64
}

type bulkEditTemplateLibraryStore struct {
	Items []bulkEditTemplateStoreItem `json:"items"`
}

type bulkEditTemplateVersionStoreItem struct {
	VersionID  string          `json:"version_id"`
	ShareScope string          `json:"share_scope"`
	GroupIDs   []int64         `json:"group_ids"`
	State      json.RawMessage `json:"state"`
	UpdatedBy  int64           `json:"updated_by"`
	UpdatedAt  int64           `json:"updated_at"`
}

type bulkEditTemplateStoreItem struct {
	ID            string                             `json:"id"`
	Name          string                             `json:"name"`
	ScopePlatform string                             `json:"scope_platform"`
	ScopeType     string                             `json:"scope_type"`
	ShareScope    string                             `json:"share_scope"`
	GroupIDs      []int64                            `json:"group_ids"`
	State         json.RawMessage                    `json:"state"`
	Versions      []bulkEditTemplateVersionStoreItem `json:"versions"`
	CreatedBy     int64                              `json:"created_by"`
	UpdatedBy     int64                              `json:"updated_by"`
	CreatedAt     int64                              `json:"created_at"`
	UpdatedAt     int64                              `json:"updated_at"`
}

func (s *SettingService) ListBulkEditTemplates(ctx context.Context, query BulkEditTemplateQuery) ([]BulkEditTemplate, error) {
	store, err := s.loadBulkEditTemplateLibrary(ctx)
	if err != nil {
		return nil, err
	}

	scopePlatform := strings.TrimSpace(strings.ToLower(query.ScopePlatform))
	scopeType := strings.TrimSpace(strings.ToLower(query.ScopeType))
	scopeGroupIDs := normalizeBulkEditTemplateGroupIDs(query.ScopeGroupIDs)
	scopeGroupSet := make(map[int64]struct{}, len(scopeGroupIDs))
	for _, groupID := range scopeGroupIDs {
		scopeGroupSet[groupID] = struct{}{}
	}

	out := make([]BulkEditTemplate, 0, len(store.Items))
	for idx := range store.Items {
		item := store.Items[idx]
		if scopePlatform != "" && item.ScopePlatform != scopePlatform {
			continue
		}
		if scopeType != "" && item.ScopeType != scopeType {
			continue
		}
		if !isBulkEditTemplateVisible(item, query.RequesterUserID, scopeGroupSet) {
			continue
		}
		out = append(out, toBulkEditTemplate(item))
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})

	return out, nil
}

func (s *SettingService) UpsertBulkEditTemplate(ctx context.Context, input BulkEditTemplateUpsertInput) (*BulkEditTemplate, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, infraerrors.BadRequest("BULK_EDIT_TEMPLATE_INVALID_INPUT", "template name is required")
	}
	if input.RequesterUserID <= 0 {
		return nil, infraerrors.Unauthorized("UNAUTHORIZED", "unauthorized")
	}

	scopePlatform := strings.TrimSpace(strings.ToLower(input.ScopePlatform))
	scopeType := strings.TrimSpace(strings.ToLower(input.ScopeType))
	if scopePlatform == "" || scopeType == "" {
		return nil, infraerrors.BadRequest(
			"BULK_EDIT_TEMPLATE_INVALID_INPUT",
			"scope_platform and scope_type are required",
		)
	}

	shareScope, shareScopeErr := validateBulkEditTemplateShareScope(input.ShareScope)
	if shareScopeErr != nil {
		return nil, shareScopeErr
	}

	groupIDs := normalizeBulkEditTemplateGroupIDs(input.GroupIDs)
	if shareScope == BulkEditTemplateShareScopeGroups && len(groupIDs) == 0 {
		return nil, infraerrors.BadRequest(
			"BULK_EDIT_TEMPLATE_INVALID_INPUT",
			"group_ids is required when share_scope=groups",
		)
	}

	stateRaw, err := json.Marshal(input.State)
	if err != nil {
		return nil, infraerrors.BadRequest("BULK_EDIT_TEMPLATE_INVALID_INPUT", "invalid template state")
	}
	if len(stateRaw) == 0 || string(stateRaw) == "null" {
		stateRaw = json.RawMessage("{}")
	}

	store, err := s.loadBulkEditTemplateLibrary(ctx)
	if err != nil {
		return nil, err
	}

	templateID := strings.TrimSpace(input.ID)
	matchIndex := -1
	if templateID != "" {
		for idx := range store.Items {
			if store.Items[idx].ID == templateID {
				matchIndex = idx
				break
			}
		}
	}

	if matchIndex < 0 {
		for idx := range store.Items {
			item := store.Items[idx]
			if item.ScopePlatform != scopePlatform || item.ScopeType != scopeType {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(item.Name), name) {
				continue
			}
			matchIndex = idx
			break
		}
	}

	nowMS := time.Now().UnixMilli()
	if matchIndex >= 0 {
		item := store.Items[matchIndex]
		if templateID != "" && item.ID != templateID {
			return nil, ErrBulkEditTemplateNotFound
		}

		previousVersion := snapshotBulkEditTemplateVersion(item)
		item.Versions = append(item.Versions, previousVersion)
		item.Name = name
		item.ScopePlatform = scopePlatform
		item.ScopeType = scopeType
		item.ShareScope = shareScope
		item.GroupIDs = groupIDs
		item.State = cloneBulkEditTemplateStateRaw(stateRaw)
		if item.CreatedBy <= 0 {
			item.CreatedBy = input.RequesterUserID
		}
		if item.CreatedAt <= 0 {
			item.CreatedAt = nowMS
		}
		item.UpdatedBy = input.RequesterUserID
		item.UpdatedAt = nowMS
		store.Items[matchIndex] = item

		if err := s.persistBulkEditTemplateLibrary(ctx, store); err != nil {
			return nil, err
		}
		output := toBulkEditTemplate(item)
		return &output, nil
	}

	if templateID == "" {
		templateID = generateBulkEditTemplateID()
	}

	created := bulkEditTemplateStoreItem{
		ID:            templateID,
		Name:          name,
		ScopePlatform: scopePlatform,
		ScopeType:     scopeType,
		ShareScope:    shareScope,
		GroupIDs:      groupIDs,
		State:         cloneBulkEditTemplateStateRaw(stateRaw),
		Versions:      []bulkEditTemplateVersionStoreItem{},
		CreatedBy:     input.RequesterUserID,
		UpdatedBy:     input.RequesterUserID,
		CreatedAt:     nowMS,
		UpdatedAt:     nowMS,
	}
	store.Items = append(store.Items, created)

	if err := s.persistBulkEditTemplateLibrary(ctx, store); err != nil {
		return nil, err
	}

	output := toBulkEditTemplate(created)
	return &output, nil
}

func (s *SettingService) DeleteBulkEditTemplate(ctx context.Context, templateID string, requesterUserID int64) error {
	id := strings.TrimSpace(templateID)
	if id == "" {
		return infraerrors.BadRequest("BULK_EDIT_TEMPLATE_INVALID_INPUT", "template id is required")
	}
	if requesterUserID <= 0 {
		return infraerrors.Unauthorized("UNAUTHORIZED", "unauthorized")
	}

	store, err := s.loadBulkEditTemplateLibrary(ctx)
	if err != nil {
		return err
	}

	idx := -1
	for index := range store.Items {
		if store.Items[index].ID == id {
			idx = index
			break
		}
	}
	if idx < 0 {
		return ErrBulkEditTemplateNotFound
	}

	target := store.Items[idx]
	if target.ShareScope == BulkEditTemplateShareScopePrivate && target.CreatedBy > 0 && target.CreatedBy != requesterUserID {
		return ErrBulkEditTemplateForbidden
	}

	store.Items = append(store.Items[:idx], store.Items[idx+1:]...)
	return s.persistBulkEditTemplateLibrary(ctx, store)
}

func (s *SettingService) ListBulkEditTemplateVersions(
	ctx context.Context,
	query BulkEditTemplateVersionQuery,
) ([]BulkEditTemplateVersion, error) {
	templateID := strings.TrimSpace(query.TemplateID)
	if templateID == "" {
		return nil, infraerrors.BadRequest("BULK_EDIT_TEMPLATE_INVALID_INPUT", "template id is required")
	}
	if query.RequesterUserID <= 0 {
		return nil, infraerrors.Unauthorized("UNAUTHORIZED", "unauthorized")
	}

	store, err := s.loadBulkEditTemplateLibrary(ctx)
	if err != nil {
		return nil, err
	}

	scopeGroupSet := toBulkEditTemplateScopeGroupSet(query.ScopeGroupIDs)
	target := findBulkEditTemplateStoreItemByID(store.Items, templateID)
	if target == nil {
		return nil, ErrBulkEditTemplateNotFound
	}
	if !isBulkEditTemplateVisible(*target, query.RequesterUserID, scopeGroupSet) {
		return nil, ErrBulkEditTemplateForbidden
	}

	versions := make([]BulkEditTemplateVersion, 0, len(target.Versions))
	for idx := range target.Versions {
		versions = append(versions, toBulkEditTemplateVersion(target.Versions[idx]))
	}

	sort.Slice(versions, func(i, j int) bool {
		if versions[i].UpdatedAt == versions[j].UpdatedAt {
			return versions[i].VersionID < versions[j].VersionID
		}
		return versions[i].UpdatedAt > versions[j].UpdatedAt
	})

	return versions, nil
}

func (s *SettingService) RollbackBulkEditTemplate(
	ctx context.Context,
	input BulkEditTemplateRollbackInput,
) (*BulkEditTemplate, error) {
	templateID := strings.TrimSpace(input.TemplateID)
	versionID := strings.TrimSpace(input.VersionID)
	if templateID == "" || versionID == "" {
		return nil, infraerrors.BadRequest("BULK_EDIT_TEMPLATE_INVALID_INPUT", "template_id and version_id are required")
	}
	if input.RequesterUserID <= 0 {
		return nil, infraerrors.Unauthorized("UNAUTHORIZED", "unauthorized")
	}

	store, err := s.loadBulkEditTemplateLibrary(ctx)
	if err != nil {
		return nil, err
	}

	scopeGroupSet := toBulkEditTemplateScopeGroupSet(input.ScopeGroupIDs)
	templateIndex := findBulkEditTemplateStoreItemIndexByID(store.Items, templateID)
	if templateIndex < 0 {
		return nil, ErrBulkEditTemplateNotFound
	}

	item := store.Items[templateIndex]
	if !isBulkEditTemplateVisible(item, input.RequesterUserID, scopeGroupSet) {
		return nil, ErrBulkEditTemplateForbidden
	}

	versionIndex := findBulkEditTemplateVersionIndexByID(item.Versions, versionID)
	if versionIndex < 0 {
		return nil, ErrBulkEditTemplateVersionNotFound
	}

	targetVersion := item.Versions[versionIndex]
	previousVersion := snapshotBulkEditTemplateVersion(item)
	item.Versions = append(item.Versions, previousVersion)
	item.ShareScope = targetVersion.ShareScope
	item.GroupIDs = append([]int64(nil), targetVersion.GroupIDs...)
	item.State = cloneBulkEditTemplateStateRaw(targetVersion.State)
	item.UpdatedBy = input.RequesterUserID
	item.UpdatedAt = time.Now().UnixMilli()

	store.Items[templateIndex] = item
	if persistErr := s.persistBulkEditTemplateLibrary(ctx, store); persistErr != nil {
		return nil, persistErr
	}

	output := toBulkEditTemplate(item)
	return &output, nil
}

func (s *SettingService) loadBulkEditTemplateLibrary(ctx context.Context) (*bulkEditTemplateLibraryStore, error) {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyBulkEditTemplateLibrary)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return &bulkEditTemplateLibraryStore{}, nil
		}
		return nil, fmt.Errorf("get bulk edit template library: %w", err)
	}

	raw = strings.TrimSpace(raw)
	if raw == "" {
		return &bulkEditTemplateLibraryStore{}, nil
	}

	store := bulkEditTemplateLibraryStore{}
	if err := json.Unmarshal([]byte(raw), &store); err != nil {
		return &bulkEditTemplateLibraryStore{}, nil
	}

	normalized := normalizeBulkEditTemplateLibraryStore(store)
	return &normalized, nil
}

func (s *SettingService) persistBulkEditTemplateLibrary(ctx context.Context, store *bulkEditTemplateLibraryStore) error {
	if store == nil {
		return infraerrors.BadRequest("BULK_EDIT_TEMPLATE_INVALID_INPUT", "template library cannot be nil")
	}

	normalized := normalizeBulkEditTemplateLibraryStore(*store)
	data, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("marshal bulk edit template library: %w", err)
	}

	return s.settingRepo.Set(ctx, SettingKeyBulkEditTemplateLibrary, string(data))
}

func validateBulkEditTemplateShareScope(scope string) (string, error) {
	normalized := strings.TrimSpace(strings.ToLower(scope))
	if normalized == "" {
		return BulkEditTemplateShareScopePrivate, nil
	}
	switch normalized {
	case BulkEditTemplateShareScopePrivate,
		BulkEditTemplateShareScopeTeam,
		BulkEditTemplateShareScopeGroups:
		return normalized, nil
	default:
		return "", infraerrors.BadRequest("BULK_EDIT_TEMPLATE_INVALID_INPUT", "invalid share_scope")
	}
}

func normalizeBulkEditTemplateLibraryStore(store bulkEditTemplateLibraryStore) bulkEditTemplateLibraryStore {
	if len(store.Items) == 0 {
		return bulkEditTemplateLibraryStore{Items: []bulkEditTemplateStoreItem{}}
	}

	nowMS := time.Now().UnixMilli()
	items := make([]bulkEditTemplateStoreItem, 0, len(store.Items))
	seenID := make(map[string]struct{}, len(store.Items))

	for _, raw := range store.Items {
		name := strings.TrimSpace(raw.Name)
		scopePlatform := strings.TrimSpace(strings.ToLower(raw.ScopePlatform))
		scopeType := strings.TrimSpace(strings.ToLower(raw.ScopeType))
		if name == "" || scopePlatform == "" || scopeType == "" {
			continue
		}

		shareScope := normalizeBulkEditTemplateShareScopeOrDefault(raw.ShareScope)
		groupIDs := normalizeBulkEditTemplateGroupIDs(raw.GroupIDs)
		if shareScope == BulkEditTemplateShareScopeGroups && len(groupIDs) == 0 {
			shareScope = BulkEditTemplateShareScopePrivate
		}

		templateID := strings.TrimSpace(raw.ID)
		if templateID == "" {
			templateID = generateBulkEditTemplateID()
		}
		if _, exists := seenID[templateID]; exists {
			continue
		}
		seenID[templateID] = struct{}{}

		state := raw.State
		if len(state) == 0 || string(state) == "null" {
			state = json.RawMessage("{}")
		}

		createdAt := raw.CreatedAt
		if createdAt <= 0 {
			createdAt = nowMS
		}
		updatedAt := raw.UpdatedAt
		if updatedAt <= 0 {
			updatedAt = createdAt
		}

		items = append(items, bulkEditTemplateStoreItem{
			ID:            templateID,
			Name:          name,
			ScopePlatform: scopePlatform,
			ScopeType:     scopeType,
			ShareScope:    shareScope,
			GroupIDs:      groupIDs,
			State:         cloneBulkEditTemplateStateRaw(state),
			Versions:      normalizeBulkEditTemplateVersionStoreItems(raw.Versions),
			CreatedBy:     raw.CreatedBy,
			UpdatedBy:     raw.UpdatedBy,
			CreatedAt:     createdAt,
			UpdatedAt:     updatedAt,
		})
	}

	return bulkEditTemplateLibraryStore{Items: items}
}

func toBulkEditTemplate(item bulkEditTemplateStoreItem) BulkEditTemplate {
	state := map[string]any{}
	if err := json.Unmarshal(item.State, &state); err != nil || state == nil {
		state = map[string]any{}
	}

	return BulkEditTemplate{
		ID:            item.ID,
		Name:          item.Name,
		ScopePlatform: item.ScopePlatform,
		ScopeType:     item.ScopeType,
		ShareScope:    item.ShareScope,
		GroupIDs:      append([]int64(nil), item.GroupIDs...),
		State:         state,
		CreatedBy:     item.CreatedBy,
		UpdatedBy:     item.UpdatedBy,
		CreatedAt:     item.CreatedAt,
		UpdatedAt:     item.UpdatedAt,
	}
}

func toBulkEditTemplateVersion(item bulkEditTemplateVersionStoreItem) BulkEditTemplateVersion {
	state := map[string]any{}
	if err := json.Unmarshal(item.State, &state); err != nil || state == nil {
		state = map[string]any{}
	}

	return BulkEditTemplateVersion{
		VersionID:  item.VersionID,
		ShareScope: item.ShareScope,
		GroupIDs:   append([]int64(nil), item.GroupIDs...),
		State:      state,
		UpdatedBy:  item.UpdatedBy,
		UpdatedAt:  item.UpdatedAt,
	}
}

func normalizeBulkEditTemplateVersionStoreItems(
	rawVersions []bulkEditTemplateVersionStoreItem,
) []bulkEditTemplateVersionStoreItem {
	if len(rawVersions) == 0 {
		return []bulkEditTemplateVersionStoreItem{}
	}

	nowMS := time.Now().UnixMilli()
	seen := make(map[string]struct{}, len(rawVersions))
	out := make([]bulkEditTemplateVersionStoreItem, 0, len(rawVersions))
	for _, raw := range rawVersions {
		versionID := strings.TrimSpace(raw.VersionID)
		if versionID == "" {
			versionID = generateBulkEditTemplateVersionID()
		}
		if _, exists := seen[versionID]; exists {
			continue
		}
		seen[versionID] = struct{}{}

		shareScope := normalizeBulkEditTemplateShareScopeOrDefault(raw.ShareScope)
		groupIDs := normalizeBulkEditTemplateGroupIDs(raw.GroupIDs)
		if shareScope == BulkEditTemplateShareScopeGroups && len(groupIDs) == 0 {
			shareScope = BulkEditTemplateShareScopePrivate
		}

		updatedAt := raw.UpdatedAt
		if updatedAt <= 0 {
			updatedAt = nowMS
		}

		out = append(out, bulkEditTemplateVersionStoreItem{
			VersionID:  versionID,
			ShareScope: shareScope,
			GroupIDs:   groupIDs,
			State:      cloneBulkEditTemplateStateRaw(raw.State),
			UpdatedBy:  raw.UpdatedBy,
			UpdatedAt:  updatedAt,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].VersionID < out[j].VersionID
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out
}

func snapshotBulkEditTemplateVersion(item bulkEditTemplateStoreItem) bulkEditTemplateVersionStoreItem {
	updatedAt := item.UpdatedAt
	if updatedAt <= 0 {
		updatedAt = time.Now().UnixMilli()
	}
	return bulkEditTemplateVersionStoreItem{
		VersionID:  generateBulkEditTemplateVersionID(),
		ShareScope: normalizeBulkEditTemplateShareScopeOrDefault(item.ShareScope),
		GroupIDs:   normalizeBulkEditTemplateGroupIDs(item.GroupIDs),
		State:      cloneBulkEditTemplateStateRaw(item.State),
		UpdatedBy:  item.UpdatedBy,
		UpdatedAt:  updatedAt,
	}
}

func cloneBulkEditTemplateStateRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage("{}")
	}
	cloned := make(json.RawMessage, len(raw))
	copy(cloned, raw)
	return cloned
}

func toBulkEditTemplateScopeGroupSet(raw []int64) map[int64]struct{} {
	groupIDs := normalizeBulkEditTemplateGroupIDs(raw)
	scopeGroupSet := make(map[int64]struct{}, len(groupIDs))
	for _, groupID := range groupIDs {
		scopeGroupSet[groupID] = struct{}{}
	}
	return scopeGroupSet
}

func findBulkEditTemplateStoreItemByID(
	items []bulkEditTemplateStoreItem,
	templateID string,
) *bulkEditTemplateStoreItem {
	for idx := range items {
		if items[idx].ID == templateID {
			return &items[idx]
		}
	}
	return nil
}

func findBulkEditTemplateStoreItemIndexByID(items []bulkEditTemplateStoreItem, templateID string) int {
	for idx := range items {
		if items[idx].ID == templateID {
			return idx
		}
	}
	return -1
}

func findBulkEditTemplateVersionIndexByID(
	versions []bulkEditTemplateVersionStoreItem,
	versionID string,
) int {
	for idx := range versions {
		if versions[idx].VersionID == versionID {
			return idx
		}
	}
	return -1
}

func isBulkEditTemplateVisible(
	item bulkEditTemplateStoreItem,
	requesterUserID int64,
	scopeGroupSet map[int64]struct{},
) bool {
	switch item.ShareScope {
	case BulkEditTemplateShareScopeTeam:
		return true
	case BulkEditTemplateShareScopeGroups:
		if len(scopeGroupSet) == 0 || len(item.GroupIDs) == 0 {
			return false
		}
		for _, groupID := range item.GroupIDs {
			if _, ok := scopeGroupSet[groupID]; ok {
				return true
			}
		}
		return false
	default:
		return requesterUserID > 0 && item.CreatedBy == requesterUserID
	}
}

func normalizeBulkEditTemplateShareScopeOrDefault(scope string) string {
	normalized, err := validateBulkEditTemplateShareScope(scope)
	if err != nil {
		return BulkEditTemplateShareScopePrivate
	}
	return normalized
}

func normalizeBulkEditTemplateGroupIDs(raw []int64) []int64 {
	if len(raw) == 0 {
		return []int64{}
	}

	seen := make(map[int64]struct{}, len(raw))
	groupIDs := make([]int64, 0, len(raw))
	for _, groupID := range raw {
		if groupID <= 0 {
			continue
		}
		if _, exists := seen[groupID]; exists {
			continue
		}
		seen[groupID] = struct{}{}
		groupIDs = append(groupIDs, groupID)
	}
	sort.Slice(groupIDs, func(i, j int) bool {
		return groupIDs[i] < groupIDs[j]
	})
	return groupIDs
}

func generateBulkEditTemplateID() string {
	buf := make([]byte, 12)
	if _, err := bulkEditTemplateRandRead(buf); err == nil {
		return "btpl-" + hex.EncodeToString(buf)
	}
	return fmt.Sprintf("btpl-%d", time.Now().UnixNano())
}

func generateBulkEditTemplateVersionID() string {
	buf := make([]byte, 12)
	if _, err := bulkEditTemplateRandRead(buf); err == nil {
		return "btplv-" + hex.EncodeToString(buf)
	}
	return fmt.Sprintf("btplv-%d", time.Now().UnixNano())
}
