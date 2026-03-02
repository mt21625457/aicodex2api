package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type bulkTemplateSettingRepoStub struct {
	values map[string]string
}

func newBulkTemplateSettingRepoStub() *bulkTemplateSettingRepoStub {
	return &bulkTemplateSettingRepoStub{values: map[string]string{}}
}

func (s *bulkTemplateSettingRepoStub) Get(ctx context.Context, key string) (*Setting, error) {
	value, err := s.GetValue(ctx, key)
	if err != nil {
		return nil, err
	}
	return &Setting{Key: key, Value: value}, nil
}

func (s *bulkTemplateSettingRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	value, ok := s.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (s *bulkTemplateSettingRepoStub) Set(ctx context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

func (s *bulkTemplateSettingRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func (s *bulkTemplateSettingRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	for key, value := range settings {
		s.values[key] = value
	}
	return nil
}

func (s *bulkTemplateSettingRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	out := make(map[string]string, len(s.values))
	for key, value := range s.values {
		out[key] = value
	}
	return out, nil
}

func (s *bulkTemplateSettingRepoStub) Delete(ctx context.Context, key string) error {
	delete(s.values, key)
	return nil
}

type bulkTemplateFailingRepoStub struct{}

func (s *bulkTemplateFailingRepoStub) Get(ctx context.Context, key string) (*Setting, error) {
	return nil, errors.New("boom")
}
func (s *bulkTemplateFailingRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	return "", errors.New("boom")
}
func (s *bulkTemplateFailingRepoStub) Set(ctx context.Context, key, value string) error {
	return errors.New("boom")
}
func (s *bulkTemplateFailingRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	return nil, errors.New("boom")
}
func (s *bulkTemplateFailingRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	return errors.New("boom")
}
func (s *bulkTemplateFailingRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	return nil, errors.New("boom")
}
func (s *bulkTemplateFailingRepoStub) Delete(ctx context.Context, key string) error {
	return errors.New("boom")
}

func TestSettingServiceBulkEditTemplate_UpsertAndPrivateVisibility(t *testing.T) {
	repo := newBulkTemplateSettingRepoStub()
	svc := NewSettingService(repo, nil)

	created, err := svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		Name:            "OpenAI OAuth Baseline",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ShareScope:      BulkEditTemplateShareScopePrivate,
		State:           map[string]any{"enableOpenAIWSMode": true},
		RequesterUserID: 11,
	})
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	require.Equal(t, BulkEditTemplateShareScopePrivate, created.ShareScope)

	listByOwner, err := svc.ListBulkEditTemplates(context.Background(), BulkEditTemplateQuery{
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		RequesterUserID: 11,
	})
	require.NoError(t, err)
	require.Len(t, listByOwner, 1)
	require.Equal(t, created.ID, listByOwner[0].ID)
	require.Equal(t, true, listByOwner[0].State["enableOpenAIWSMode"])

	listByOther, err := svc.ListBulkEditTemplates(context.Background(), BulkEditTemplateQuery{
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		RequesterUserID: 22,
	})
	require.NoError(t, err)
	require.Len(t, listByOther, 0)
}

func TestSettingServiceBulkEditTemplate_GroupsVisibilityByScopeGroupIDs(t *testing.T) {
	repo := newBulkTemplateSettingRepoStub()
	svc := NewSettingService(repo, nil)

	_, err := svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		Name:            "Shared By Group",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ShareScope:      BulkEditTemplateShareScopeGroups,
		GroupIDs:        []int64{10, 20},
		State:           map[string]any{"enableBaseUrl": true},
		RequesterUserID: 9,
	})
	require.NoError(t, err)

	invisible, err := svc.ListBulkEditTemplates(context.Background(), BulkEditTemplateQuery{
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ScopeGroupIDs:   []int64{99},
		RequesterUserID: 8,
	})
	require.NoError(t, err)
	require.Len(t, invisible, 0)

	visible, err := svc.ListBulkEditTemplates(context.Background(), BulkEditTemplateQuery{
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ScopeGroupIDs:   []int64{20, 100},
		RequesterUserID: 8,
	})
	require.NoError(t, err)
	require.Len(t, visible, 1)
	require.Equal(t, BulkEditTemplateShareScopeGroups, visible[0].ShareScope)
	require.Equal(t, []int64{10, 20}, visible[0].GroupIDs)
}

func TestSettingServiceBulkEditTemplate_UpsertByNameReplacesSameScopeRecord(t *testing.T) {
	repo := newBulkTemplateSettingRepoStub()
	svc := NewSettingService(repo, nil)

	first, err := svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		Name:            "Team Baseline",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ShareScope:      BulkEditTemplateShareScopeTeam,
		State:           map[string]any{"priority": 1},
		RequesterUserID: 7,
	})
	require.NoError(t, err)

	second, err := svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		Name:            "team baseline",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ShareScope:      BulkEditTemplateShareScopeTeam,
		State:           map[string]any{"priority": 9},
		RequesterUserID: 7,
	})
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)

	items, err := svc.ListBulkEditTemplates(context.Background(), BulkEditTemplateQuery{
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		RequesterUserID: 3,
	})
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.EqualValues(t, 9, items[0].State["priority"])
}

func TestSettingServiceBulkEditTemplate_DeletePermissionAndNotFound(t *testing.T) {
	repo := newBulkTemplateSettingRepoStub()
	svc := NewSettingService(repo, nil)

	created, err := svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		Name:            "Private Template",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ShareScope:      BulkEditTemplateShareScopePrivate,
		State:           map[string]any{"status": "active"},
		RequesterUserID: 1,
	})
	require.NoError(t, err)

	err = svc.DeleteBulkEditTemplate(context.Background(), created.ID, 2)
	require.Error(t, err)
	require.True(t, infraerrors.IsForbidden(err))

	err = svc.DeleteBulkEditTemplate(context.Background(), created.ID, 1)
	require.NoError(t, err)

	ownerList, err := svc.ListBulkEditTemplates(context.Background(), BulkEditTemplateQuery{
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		RequesterUserID: 1,
	})
	require.NoError(t, err)
	require.Len(t, ownerList, 0)

	err = svc.DeleteBulkEditTemplate(context.Background(), "missing-id", 1)
	require.Error(t, err)
	require.True(t, infraerrors.IsNotFound(err))
}

func TestSettingServiceBulkEditTemplate_ValidatesInput(t *testing.T) {
	repo := newBulkTemplateSettingRepoStub()
	svc := NewSettingService(repo, nil)

	_, err := svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		Name:            "",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		RequesterUserID: 1,
	})
	require.Error(t, err)
	require.True(t, infraerrors.IsBadRequest(err))

	_, err = svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		Name:            "Groups No IDs",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ShareScope:      BulkEditTemplateShareScopeGroups,
		RequesterUserID: 1,
	})
	require.Error(t, err)
	require.True(t, infraerrors.IsBadRequest(err))

	_, err = svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		Name:            "Invalid Scope",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ShareScope:      "invalid",
		RequesterUserID: 1,
	})
	require.Error(t, err)
	require.True(t, infraerrors.IsBadRequest(err))
}

func TestSettingServiceBulkEditTemplate_CoversInternalHelpers(t *testing.T) {
	store := normalizeBulkEditTemplateLibraryStore(bulkEditTemplateLibraryStore{
		Items: []bulkEditTemplateStoreItem{
			{
				ID:            "same-id",
				Name:          " One ",
				ScopePlatform: "OPENAI",
				ScopeType:     "OAUTH",
				ShareScope:    BulkEditTemplateShareScopeGroups,
				GroupIDs:      []int64{},
				State:         nil,
			},
			{
				ID:            "same-id",
				Name:          "Duplicate ID",
				ScopePlatform: "openai",
				ScopeType:     "oauth",
				ShareScope:    BulkEditTemplateShareScopeTeam,
			},
			{
				ID:            "",
				Name:          "Two",
				ScopePlatform: "openai",
				ScopeType:     "apikey",
				ShareScope:    "invalid",
				GroupIDs:      []int64{5, 5, 1},
				State:         []byte(`{"ok":true}`),
			},
			{
				ID:            "invalid-entry",
				Name:          "",
				ScopePlatform: "openai",
				ScopeType:     "oauth",
			},
		},
	})
	require.Len(t, store.Items, 2)
	require.Equal(t, "same-id", store.Items[0].ID)
	require.Equal(t, BulkEditTemplateShareScopePrivate, store.Items[0].ShareScope)
	require.Equal(t, []int64{1, 5}, store.Items[1].GroupIDs)
	require.NotEmpty(t, store.Items[1].ID)

	require.Equal(t, BulkEditTemplateShareScopeTeam, normalizeBulkEditTemplateShareScopeOrDefault("team"))
	require.Equal(t, BulkEditTemplateShareScopePrivate, normalizeBulkEditTemplateShareScopeOrDefault("bad"))
	require.Equal(t, []int64{}, normalizeBulkEditTemplateGroupIDs(nil))

	scope, err := validateBulkEditTemplateShareScope("")
	require.NoError(t, err)
	require.Equal(t, BulkEditTemplateShareScopePrivate, scope)
	_, err = validateBulkEditTemplateShareScope("bad")
	require.Error(t, err)
	require.True(t, infraerrors.IsBadRequest(err))

	require.True(t, isBulkEditTemplateVisible(
		bulkEditTemplateStoreItem{ShareScope: BulkEditTemplateShareScopeTeam},
		1,
		map[int64]struct{}{},
	))
	require.False(t, isBulkEditTemplateVisible(
		bulkEditTemplateStoreItem{ShareScope: BulkEditTemplateShareScopeGroups, GroupIDs: []int64{2}},
		1,
		map[int64]struct{}{},
	))
	require.True(t, isBulkEditTemplateVisible(
		bulkEditTemplateStoreItem{ShareScope: BulkEditTemplateShareScopeGroups, GroupIDs: []int64{2}},
		1,
		map[int64]struct{}{2: {}},
	))
	require.True(t, isBulkEditTemplateVisible(
		bulkEditTemplateStoreItem{ShareScope: BulkEditTemplateShareScopePrivate, CreatedBy: 9},
		9,
		nil,
	))
	require.False(t, isBulkEditTemplateVisible(
		bulkEditTemplateStoreItem{ShareScope: BulkEditTemplateShareScopePrivate, CreatedBy: 9},
		1,
		nil,
	))

	converted := toBulkEditTemplate(bulkEditTemplateStoreItem{
		ID:            "id-1",
		Name:          "Demo",
		ScopePlatform: "openai",
		ScopeType:     "oauth",
		ShareScope:    BulkEditTemplateShareScopePrivate,
		GroupIDs:      []int64{3},
		State:         []byte(`invalid-json`),
	})
	require.Equal(t, map[string]any{}, converted.State)
}

func TestSettingServiceBulkEditTemplate_LoadPersistBranches(t *testing.T) {
	repo := newBulkTemplateSettingRepoStub()
	svc := NewSettingService(repo, nil)

	err := svc.persistBulkEditTemplateLibrary(context.Background(), nil)
	require.Error(t, err)
	require.True(t, infraerrors.IsBadRequest(err))

	repo.values[SettingKeyBulkEditTemplateLibrary] = "{bad-json"
	store, err := svc.loadBulkEditTemplateLibrary(context.Background())
	require.Error(t, err)
	require.Nil(t, store)

	delete(repo.values, SettingKeyBulkEditTemplateLibrary)
	store, err = svc.loadBulkEditTemplateLibrary(context.Background())
	require.NoError(t, err)
	require.NotNil(t, store)
	require.Empty(t, store.Items)
}

func TestSettingServiceBulkEditTemplate_UpsertByMismatchedID(t *testing.T) {
	repo := newBulkTemplateSettingRepoStub()
	svc := NewSettingService(repo, nil)

	created, err := svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		Name:            "Scoped Template",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ShareScope:      BulkEditTemplateShareScopePrivate,
		State:           map[string]any{},
		RequesterUserID: 1,
	})
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)

	_, err = svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		ID:              "another-id",
		Name:            "Scoped Template",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ShareScope:      BulkEditTemplateShareScopePrivate,
		State:           map[string]any{},
		RequesterUserID: 1,
	})
	require.Error(t, err)
	require.True(t, infraerrors.IsNotFound(err))

	err = svc.DeleteBulkEditTemplate(context.Background(), "", 1)
	require.Error(t, err)
	require.True(t, infraerrors.IsBadRequest(err))
}

func TestSettingServiceBulkEditTemplate_PrivateTemplateIsolationAcrossUsers(t *testing.T) {
	repo := newBulkTemplateSettingRepoStub()
	svc := NewSettingService(repo, nil)

	ownerTemplate, err := svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		Name:            "Private Scoped Template",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ShareScope:      BulkEditTemplateShareScopePrivate,
		State:           map[string]any{"priority": 1},
		RequesterUserID: 101,
	})
	require.NoError(t, err)

	otherTemplate, err := svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		Name:            "Private Scoped Template",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ShareScope:      BulkEditTemplateShareScopePrivate,
		State:           map[string]any{"priority": 9},
		RequesterUserID: 202,
	})
	require.NoError(t, err)
	require.NotEqual(t, ownerTemplate.ID, otherTemplate.ID, "不同用户的私有同名模板不应互相覆盖")

	ownerVisible, err := svc.ListBulkEditTemplates(context.Background(), BulkEditTemplateQuery{
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		RequesterUserID: 101,
	})
	require.NoError(t, err)
	require.Len(t, ownerVisible, 1)
	require.Equal(t, ownerTemplate.ID, ownerVisible[0].ID)
	require.EqualValues(t, 1, ownerVisible[0].State["priority"])

	otherVisible, err := svc.ListBulkEditTemplates(context.Background(), BulkEditTemplateQuery{
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		RequesterUserID: 202,
	})
	require.NoError(t, err)
	require.Len(t, otherVisible, 1)
	require.Equal(t, otherTemplate.ID, otherVisible[0].ID)
	require.EqualValues(t, 9, otherVisible[0].State["priority"])

	_, err = svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		ID:              ownerTemplate.ID,
		Name:            ownerTemplate.Name,
		ScopePlatform:   ownerTemplate.ScopePlatform,
		ScopeType:       ownerTemplate.ScopeType,
		ShareScope:      BulkEditTemplateShareScopePrivate,
		State:           map[string]any{"priority": 99},
		RequesterUserID: 202,
	})
	require.Error(t, err)
	require.True(t, infraerrors.IsForbidden(err), "非 owner 不允许通过 template ID 修改私有模板")
}

func TestSettingServiceBulkEditTemplate_UpsertFailsWhenStoredLibraryCorrupted(t *testing.T) {
	repo := newBulkTemplateSettingRepoStub()
	repo.values[SettingKeyBulkEditTemplateLibrary] = "{bad-json"
	svc := NewSettingService(repo, nil)

	_, err := svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		Name:            "Should Fail",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ShareScope:      BulkEditTemplateShareScopePrivate,
		State:           map[string]any{"ok": true},
		RequesterUserID: 1,
	})
	require.Error(t, err)
}

func TestSettingServiceBulkEditTemplate_ListFilteringAndSorting(t *testing.T) {
	repo := newBulkTemplateSettingRepoStub()
	svc := NewSettingService(repo, nil)

	store := bulkEditTemplateLibraryStore{
		Items: []bulkEditTemplateStoreItem{
			{
				ID:            "b",
				Name:          "Second",
				ScopePlatform: "openai",
				ScopeType:     "oauth",
				ShareScope:    BulkEditTemplateShareScopeTeam,
				State:         []byte(`{}`),
				CreatedBy:     1,
				UpdatedBy:     1,
				CreatedAt:     1,
				UpdatedAt:     100,
			},
			{
				ID:            "a",
				Name:          "First",
				ScopePlatform: "openai",
				ScopeType:     "oauth",
				ShareScope:    BulkEditTemplateShareScopeTeam,
				State:         []byte(`{}`),
				CreatedBy:     1,
				UpdatedBy:     1,
				CreatedAt:     1,
				UpdatedAt:     100,
			},
			{
				ID:            "skip-type",
				Name:          "Skip Type",
				ScopePlatform: "openai",
				ScopeType:     "apikey",
				ShareScope:    BulkEditTemplateShareScopeTeam,
				State:         []byte(`{}`),
				CreatedBy:     1,
				UpdatedBy:     1,
				CreatedAt:     1,
				UpdatedAt:     999,
			},
			{
				ID:            "skip-private",
				Name:          "Skip Private",
				ScopePlatform: "openai",
				ScopeType:     "oauth",
				ShareScope:    BulkEditTemplateShareScopePrivate,
				State:         []byte(`{}`),
				CreatedBy:     99,
				UpdatedBy:     99,
				CreatedAt:     1,
				UpdatedAt:     1000,
			},
		},
	}
	require.NoError(t, svc.persistBulkEditTemplateLibrary(context.Background(), &store))

	items, err := svc.ListBulkEditTemplates(context.Background(), BulkEditTemplateQuery{
		ScopePlatform:   "openai",
		ScopeType:       "oauth",
		RequesterUserID: 1,
	})
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, []string{"a", "b"}, []string{items[0].ID, items[1].ID})
}

func TestSettingServiceBulkEditTemplate_UpsertByIDAndMarshalError(t *testing.T) {
	repo := newBulkTemplateSettingRepoStub()
	svc := NewSettingService(repo, nil)

	created, err := svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		Name:            "By ID",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ShareScope:      BulkEditTemplateShareScopeTeam,
		State:           map[string]any{"priority": 1},
		RequesterUserID: 1,
	})
	require.NoError(t, err)

	updated, err := svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		ID:              created.ID,
		Name:            "By ID",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ShareScope:      BulkEditTemplateShareScopeTeam,
		State:           map[string]any{"priority": 2},
		RequesterUserID: 1,
	})
	require.NoError(t, err)
	require.Equal(t, created.ID, updated.ID)
	require.EqualValues(t, 2, updated.State["priority"])

	_, err = svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		Name:            "Marshal Error",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ShareScope:      BulkEditTemplateShareScopePrivate,
		State:           map[string]any{"bad": make(chan int)},
		RequesterUserID: 1,
	})
	require.Error(t, err)
	require.True(t, infraerrors.IsBadRequest(err))

	_, err = svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		Name:            "Unauthorized",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ShareScope:      BulkEditTemplateShareScopePrivate,
		State:           map[string]any{},
		RequesterUserID: 0,
	})
	require.Error(t, err)
	require.True(t, infraerrors.IsUnauthorized(err))

	_, err = svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		Name:            "Missing Scope",
		ScopePlatform:   "",
		ScopeType:       AccountTypeOAuth,
		ShareScope:      BulkEditTemplateShareScopePrivate,
		State:           map[string]any{},
		RequesterUserID: 1,
	})
	require.Error(t, err)
	require.True(t, infraerrors.IsBadRequest(err))
}

func TestSettingServiceBulkEditTemplate_LoadErrorFromRepository(t *testing.T) {
	svc := NewSettingService(&bulkTemplateFailingRepoStub{}, nil)
	store, err := svc.loadBulkEditTemplateLibrary(context.Background())
	require.Error(t, err)
	require.Nil(t, store)
}

func TestGenerateBulkEditTemplateID_Fallback(t *testing.T) {
	original := bulkEditTemplateRandRead
	bulkEditTemplateRandRead = func(_ []byte) (int, error) {
		return 0, errors.New("rand fail")
	}
	defer func() {
		bulkEditTemplateRandRead = original
	}()

	id := generateBulkEditTemplateID()
	require.NotEmpty(t, id)
	require.Contains(t, id, "btpl-")

	versionID := generateBulkEditTemplateVersionID()
	require.NotEmpty(t, versionID)
	require.Contains(t, versionID, "btplv-")
}

func TestSettingServiceBulkEditTemplate_VersionLifecycleAndRollback(t *testing.T) {
	repo := newBulkTemplateSettingRepoStub()
	svc := NewSettingService(repo, nil)

	created, err := svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		Name:            "Versioned Template",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ShareScope:      BulkEditTemplateShareScopePrivate,
		State:           map[string]any{"priority": 1},
		RequesterUserID: 88,
	})
	require.NoError(t, err)

	updated, err := svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		ID:              created.ID,
		Name:            "Versioned Template",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ShareScope:      BulkEditTemplateShareScopeTeam,
		State:           map[string]any{"priority": 9},
		RequesterUserID: 88,
	})
	require.NoError(t, err)
	require.Equal(t, BulkEditTemplateShareScopeTeam, updated.ShareScope)

	versions, err := svc.ListBulkEditTemplateVersions(context.Background(), BulkEditTemplateVersionQuery{
		TemplateID:      created.ID,
		RequesterUserID: 88,
	})
	require.NoError(t, err)
	require.Len(t, versions, 1)
	require.Equal(t, BulkEditTemplateShareScopePrivate, versions[0].ShareScope)
	require.EqualValues(t, 1, versions[0].State["priority"])

	rollbacked, err := svc.RollbackBulkEditTemplate(context.Background(), BulkEditTemplateRollbackInput{
		TemplateID:      created.ID,
		VersionID:       versions[0].VersionID,
		RequesterUserID: 88,
	})
	require.NoError(t, err)
	require.Equal(t, BulkEditTemplateShareScopePrivate, rollbacked.ShareScope)
	require.EqualValues(t, 1, rollbacked.State["priority"])

	versionsAfterRollback, err := svc.ListBulkEditTemplateVersions(context.Background(), BulkEditTemplateVersionQuery{
		TemplateID:      created.ID,
		RequesterUserID: 88,
	})
	require.NoError(t, err)
	require.Len(t, versionsAfterRollback, 2)
}

func TestSettingServiceBulkEditTemplate_VersionVisibilityAndErrors(t *testing.T) {
	repo := newBulkTemplateSettingRepoStub()
	svc := NewSettingService(repo, nil)

	created, err := svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		Name:            "Group Template",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ShareScope:      BulkEditTemplateShareScopeGroups,
		GroupIDs:        []int64{7},
		State:           map[string]any{"enableBaseUrl": true},
		RequesterUserID: 1,
	})
	require.NoError(t, err)

	_, err = svc.UpsertBulkEditTemplate(context.Background(), BulkEditTemplateUpsertInput{
		ID:              created.ID,
		Name:            "Group Template",
		ScopePlatform:   PlatformOpenAI,
		ScopeType:       AccountTypeOAuth,
		ShareScope:      BulkEditTemplateShareScopeGroups,
		GroupIDs:        []int64{7, 9},
		State:           map[string]any{"enableBaseUrl": false},
		RequesterUserID: 1,
	})
	require.NoError(t, err)

	_, err = svc.ListBulkEditTemplateVersions(context.Background(), BulkEditTemplateVersionQuery{
		TemplateID:      created.ID,
		ScopeGroupIDs:   []int64{8},
		RequesterUserID: 2,
	})
	require.Error(t, err)
	require.True(t, infraerrors.IsForbidden(err))

	visibleVersions, err := svc.ListBulkEditTemplateVersions(context.Background(), BulkEditTemplateVersionQuery{
		TemplateID:      created.ID,
		ScopeGroupIDs:   []int64{7},
		RequesterUserID: 2,
	})
	require.NoError(t, err)
	require.Len(t, visibleVersions, 1)

	_, err = svc.ListBulkEditTemplateVersions(context.Background(), BulkEditTemplateVersionQuery{
		TemplateID:      "missing",
		RequesterUserID: 1,
	})
	require.Error(t, err)
	require.True(t, infraerrors.IsNotFound(err))

	_, err = svc.ListBulkEditTemplateVersions(context.Background(), BulkEditTemplateVersionQuery{
		TemplateID:      created.ID,
		RequesterUserID: 0,
	})
	require.Error(t, err)
	require.True(t, infraerrors.IsUnauthorized(err))

	_, err = svc.RollbackBulkEditTemplate(context.Background(), BulkEditTemplateRollbackInput{
		TemplateID:      created.ID,
		VersionID:       "missing-version",
		ScopeGroupIDs:   []int64{7},
		RequesterUserID: 2,
	})
	require.Error(t, err)
	require.True(t, infraerrors.IsNotFound(err))

	_, err = svc.RollbackBulkEditTemplate(context.Background(), BulkEditTemplateRollbackInput{
		TemplateID:      created.ID,
		VersionID:       visibleVersions[0].VersionID,
		ScopeGroupIDs:   []int64{8},
		RequesterUserID: 2,
	})
	require.Error(t, err)
	require.True(t, infraerrors.IsForbidden(err))

	_, err = svc.ListBulkEditTemplateVersions(context.Background(), BulkEditTemplateVersionQuery{
		TemplateID:      " ",
		RequesterUserID: 1,
	})
	require.Error(t, err)
	require.True(t, infraerrors.IsBadRequest(err))

	_, err = svc.RollbackBulkEditTemplate(context.Background(), BulkEditTemplateRollbackInput{
		TemplateID:      created.ID,
		VersionID:       visibleVersions[0].VersionID,
		RequesterUserID: 0,
	})
	require.Error(t, err)
	require.True(t, infraerrors.IsUnauthorized(err))
}

func TestSettingServiceBulkEditTemplate_VersionHelpers(t *testing.T) {
	normalized := normalizeBulkEditTemplateVersionStoreItems([]bulkEditTemplateVersionStoreItem{
		{
			VersionID:  "",
			ShareScope: "groups",
			GroupIDs:   []int64{},
			State:      nil,
			UpdatedBy:  1,
			UpdatedAt:  0,
		},
		{
			VersionID:  "v-1",
			ShareScope: "team",
			GroupIDs:   []int64{4, 4, 2},
			State:      []byte(`{"ok":true}`),
			UpdatedBy:  2,
			UpdatedAt:  20,
		},
		{
			VersionID:  "v-1",
			ShareScope: "team",
			State:      []byte(`{}`),
			UpdatedAt:  30,
		},
	})
	require.Len(t, normalized, 2)
	privateCount := 0
	teamCount := 0
	for _, item := range normalized {
		if item.ShareScope == BulkEditTemplateShareScopePrivate {
			privateCount++
		}
		if item.ShareScope == BulkEditTemplateShareScopeTeam {
			teamCount++
			require.Equal(t, []int64{2, 4}, item.GroupIDs)
		}
	}
	require.Equal(t, 1, privateCount)
	require.Equal(t, 1, teamCount)

	item := bulkEditTemplateStoreItem{
		ID:         "tpl-1",
		ShareScope: BulkEditTemplateShareScopeTeam,
		GroupIDs:   []int64{3},
		State:      []byte(`{"priority":3}`),
		UpdatedBy:  10,
		UpdatedAt:  123,
	}
	version := snapshotBulkEditTemplateVersion(item)
	require.NotEmpty(t, version.VersionID)
	require.Equal(t, BulkEditTemplateShareScopeTeam, version.ShareScope)
	require.EqualValues(t, 123, version.UpdatedAt)

	versionDTO := toBulkEditTemplateVersion(bulkEditTemplateVersionStoreItem{
		VersionID:  "ver-1",
		ShareScope: BulkEditTemplateShareScopePrivate,
		GroupIDs:   []int64{9},
		State:      []byte(`invalid`),
		UpdatedBy:  1,
		UpdatedAt:  2,
	})
	require.Equal(t, map[string]any{}, versionDTO.State)

	require.Equal(t, -1, findBulkEditTemplateVersionIndexByID(nil, "x"))
	require.Equal(t, -1, findBulkEditTemplateStoreItemIndexByID(nil, "x"))
	require.Nil(t, findBulkEditTemplateStoreItemByID(nil, "x"))

	scopeSet := toBulkEditTemplateScopeGroupSet([]int64{4, 4, 2, -1})
	_, has2 := scopeSet[2]
	_, has4 := scopeSet[4]
	require.True(t, has2)
	require.True(t, has4)

	cloned := cloneBulkEditTemplateStateRaw(json.RawMessage(`{"x":1}`))
	require.Equal(t, `{"x":1}`, string(cloned))
	require.Equal(t, `{}`, string(cloneBulkEditTemplateStateRaw(nil)))
}
