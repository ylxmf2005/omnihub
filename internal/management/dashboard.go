package management

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/repository"
)

var (
	ErrInvalidChannel    = errors.New("invalid channel")
	ErrInvalidEndpoint   = errors.New("invalid endpoint")
	ErrInvalidCollection = errors.New("invalid collection")
)

// ApplyEndpointInput 是 Dashboard 的通用 Endpoint 写入合同。Provider 只
// 决定转交哪个既有写入口，具体 URL、出口和 trust 校验仍由该入口负责。
type ApplyEndpointInput struct {
	ID               string `json:"id"`
	Provider         string `json:"provider"`
	BaseURL          string `json:"base_url"`
	EgressProfileID  string `json:"egress_profile_id"`
	Trust            string `json:"trust,omitempty"`
	ExpectedRevision int64  `json:"expected_revision"`
}

// ApplyChannelInput 覆盖首批可管理 RouteTemplate 的字段并保留各路线的
// 专用形状。分派时不会忽略不属于所选路线的配置字段。
type ApplyChannelInput struct {
	ID                 string             `json:"id"`
	DisplayName        string             `json:"display_name,omitempty"`
	SourceID           string             `json:"source_id"`
	SourceDisplayName  string             `json:"source_display_name,omitempty"`
	SourceCanonicalURL string             `json:"source_canonical_url,omitempty"`
	RouteTemplateID    string             `json:"route_template_id"`
	URL                string             `json:"url,omitempty"`
	EndpointProfileID  string             `json:"endpoint_profile_id,omitempty"`
	EgressProfileID    string             `json:"egress_profile_id,omitempty"`
	CredentialID       string             `json:"credential_id,omitempty"`
	Parameters         map[string]any     `json:"parameters,omitempty"`
	Priority           int                `json:"priority"`
	FallbackChannelIDs []string           `json:"fallback_channel_ids,omitempty"`
	CollectionIDs      []string           `json:"collection_ids,omitempty"`
	Enabled            bool               `json:"enabled"`
	ExpectedRevision   int64              `json:"expected_revision"`
	FeedMetadata       *core.FeedMetadata `json:"feed_metadata,omitempty"`
}

// ApplyCollectionInput 是 user-owned Collection 的完整替换输入。创建时
// ExpectedRevision 必须为 0，更新时必须精确匹配当前资源 revision。
type ApplyCollectionInput struct {
	ID               string   `json:"id"`
	Title            string   `json:"title,omitempty"`
	ParentID         string   `json:"parent_id,omitempty"`
	Position         int      `json:"position"`
	ChannelIDs       []string `json:"channel_ids"`
	Enabled          bool     `json:"enabled"`
	ExpectedRevision int64    `json:"expected_revision"`
}

// ApplyEndpoint 把 Dashboard 的统一输入分派给 RSSHub、固定官方 Provider
// 或用户拥有的 embedding Endpoint，不复制这些入口的 URL 与出口校验。
func (service Service) ApplyEndpoint(ctx context.Context, input ApplyEndpointInput) (core.EndpointProfile, error) {
	provider := strings.TrimSpace(input.Provider)
	if provider == "rsshub" {
		return service.ApplyEndpointProfile(ctx, ApplyEndpointProfileInput{
			ID: input.ID, BaseURL: input.BaseURL, EgressProfileID: input.EgressProfileID,
			Trust: input.Trust, ExpectedRevision: input.ExpectedRevision,
		})
	}
	trust := strings.TrimSpace(input.Trust)
	if provider == "embedding" {
		if trust != "" && trust != "user" {
			return core.EndpointProfile{}, fmt.Errorf("%w: embedding endpoint trust must be user", ErrInvalidEndpoint)
		}
	} else if trust != "" && trust != "official" {
		return core.EndpointProfile{}, fmt.Errorf("%w: official provider endpoint trust cannot be overridden", ErrInvalidEndpoint)
	}
	return service.ApplyProviderEndpoint(ctx, ApplyProviderEndpointInput{
		ID: input.ID, Provider: provider, BaseURL: input.BaseURL,
		EgressProfileID: input.EgressProfileID, ExpectedRevision: input.ExpectedRevision,
	})
}

// ApplyChannel 只识别模板路线并转换字段；URL、参数、Credential、出口、
// fallback 与 Collection 引用继续由既有专用方法和 Catalog 校验。
func (service Service) ApplyChannel(ctx context.Context, input ApplyChannelInput) (core.Channel, error) {
	if service.Catalog == nil {
		return core.Channel{}, errors.New("management catalog is required")
	}
	templateID := strings.TrimSpace(input.RouteTemplateID)
	if templateID == "" {
		templateID = DirectFeedRouteTemplateID
	}
	template, ok := service.Catalog.RouteTemplate(templateID)
	if !ok {
		return core.Channel{}, fmt.Errorf("%w: route template %s does not exist", ErrUnsupportedTemplate, templateID)
	}

	fallbacks := slices.Clone(input.FallbackChannelIDs)
	if fallbacks == nil {
		fallbacks = []string{}
	}
	collections := slices.Clone(input.CollectionIDs)
	if collections == nil {
		collections = []string{}
	}

	switch template.Adapter {
	case "feed":
		if strings.TrimSpace(input.EndpointProfileID) != "" || strings.TrimSpace(input.CredentialID) != "" || len(input.Parameters) != 0 {
			return core.Channel{}, fmt.Errorf("%w: Direct Feed accepts URL and egress, not endpoint, credential, or parameters", ErrInvalidChannel)
		}
		enabled := input.Enabled
		return service.ApplyDirectFeed(ctx, ApplyDirectFeedInput{
			SourceID: input.SourceID, SourceDisplayName: input.SourceDisplayName,
			SourceCanonicalURL: input.SourceCanonicalURL, ChannelID: input.ID,
			ChannelDisplayName: input.DisplayName, RouteTemplateID: templateID,
			EgressProfileID: input.EgressProfileID, URL: input.URL, Priority: input.Priority,
			FallbackChannelIDs: fallbacks, CollectionIDs: collections, Enabled: &enabled,
			ExpectedRevision: input.ExpectedRevision, FeedMetadata: input.FeedMetadata,
		})
	case "rsshub":
		if strings.TrimSpace(input.URL) != "" || strings.TrimSpace(input.EgressProfileID) != "" || strings.TrimSpace(input.SourceDisplayName) != "" || strings.TrimSpace(input.SourceCanonicalURL) != "" || input.FeedMetadata != nil {
			return core.Channel{}, fmt.Errorf("%w: RSSHub accepts endpoint and parameters, not Direct Feed fields", ErrInvalidChannel)
		}
		return service.ApplyRSSHubChannel(ctx, ApplyRSSHubChannelInput{
			ID: input.ID, DisplayName: input.DisplayName, SourceID: input.SourceID,
			RouteTemplateID: templateID, EndpointProfileID: input.EndpointProfileID,
			CredentialID: input.CredentialID, Parameters: input.Parameters, Priority: input.Priority,
			FallbackChannelIDs: fallbacks, CollectionIDs: collections, Enabled: input.Enabled,
			ExpectedRevision: input.ExpectedRevision,
		})
	case "github", "tavily", "xurl", "discourse", "arxiv", "hn_algolia":
		if strings.TrimSpace(input.URL) != "" || strings.TrimSpace(input.SourceDisplayName) != "" || strings.TrimSpace(input.SourceCanonicalURL) != "" || input.FeedMetadata != nil {
			return core.Channel{}, fmt.Errorf("%w: Provider Channel does not accept Direct Feed fields", ErrInvalidChannel)
		}
		return service.ApplyProviderChannel(ctx, ApplyProviderChannelInput{
			ID: input.ID, DisplayName: input.DisplayName, SourceID: input.SourceID,
			RouteTemplateID: templateID, EndpointProfileID: input.EndpointProfileID,
			EgressProfileID: input.EgressProfileID, CredentialID: input.CredentialID,
			Parameters: input.Parameters, Priority: input.Priority, FallbackChannelIDs: fallbacks,
			CollectionIDs: collections, Enabled: input.Enabled, ExpectedRevision: input.ExpectedRevision,
		})
	default:
		return core.Channel{}, fmt.Errorf("%w: adapter %s is not managed by Dashboard", ErrUnsupportedTemplate, template.Adapter)
	}
}

// ApplyCollection 使用资源 revision 与 aggregate Catalog revision 两层 CAS。
func (service Service) ApplyCollection(ctx context.Context, input ApplyCollectionInput) (core.Collection, error) {
	if err := service.requireStore(); err != nil {
		return core.Collection{}, err
	}
	id, parentID := strings.TrimSpace(input.ID), strings.TrimSpace(input.ParentID)
	if err := validateResourceID(id); err != nil || input.ExpectedRevision < 0 {
		return core.Collection{}, fmt.Errorf("%w: id or expected revision is invalid", ErrInvalidCollection)
	}
	if parentID == id {
		return core.Collection{}, fmt.Errorf("%w: collection cannot be its own parent", ErrInvalidCollection)
	}

	routing, err := service.Store.LoadRoutingCatalog(ctx)
	if err != nil {
		return core.Collection{}, fmt.Errorf("load routing catalog: %w", err)
	}
	working := cloneRoutingCatalog(routing)
	collectionIndex := -1
	for index, collection := range working.Collections {
		if collection.ID == id {
			collectionIndex = index
			break
		}
	}
	if collectionIndex >= 0 && working.Collections[collectionIndex].Revision != input.ExpectedRevision {
		return core.Collection{}, fmt.Errorf("%w: collection %s revision is %d, expected %d", repository.ErrConflict, id, working.Collections[collectionIndex].Revision, input.ExpectedRevision)
	}
	if collectionIndex < 0 && input.ExpectedRevision != 0 {
		return core.Collection{}, fmt.Errorf("%w: collection %s does not exist at revision %d", repository.ErrConflict, id, input.ExpectedRevision)
	}

	if parentID != "" {
		if err := validateResourceID(parentID); err != nil {
			return core.Collection{}, fmt.Errorf("%w: parent id is invalid", ErrInvalidCollection)
		}
		found := false
		for _, collection := range working.Collections {
			if collection.ID == parentID {
				found = true
				break
			}
		}
		if !found {
			return core.Collection{}, fmt.Errorf("%w: parent collection %s", repository.ErrNotFound, parentID)
		}
	}

	channelIndex := indexChannels(working.Channels)
	channelIDs := make([]string, 0, len(input.ChannelIDs))
	seen := make(map[string]bool, len(input.ChannelIDs))
	for _, rawID := range input.ChannelIDs {
		channelID := strings.TrimSpace(rawID)
		if err := validateResourceID(channelID); err != nil || seen[channelID] {
			return core.Collection{}, fmt.Errorf("%w: channel membership is invalid", ErrInvalidCollection)
		}
		if _, exists := channelIndex[channelID]; !exists {
			return core.Collection{}, fmt.Errorf("%w: channel %s", repository.ErrNotFound, channelID)
		}
		seen[channelID] = true
		channelIDs = append(channelIDs, channelID)
	}
	sort.Strings(channelIDs)
	collection := core.Collection{
		ID: id, Title: strings.TrimSpace(input.Title), ParentID: parentID, Position: input.Position,
		ChannelIDs: channelIDs, Enabled: input.Enabled,
	}
	if collectionIndex >= 0 {
		collection.Revision = working.Collections[collectionIndex].Revision + 1
		working.Collections[collectionIndex] = collection
	} else {
		collection.Revision = 1
		working.Collections = append(working.Collections, collection)
	}

	sortRoutingResources(&working)
	saved, err := service.saveRoutingCatalog(ctx, routing.Revision, working)
	if err != nil {
		return core.Collection{}, err
	}
	for _, value := range saved.Collections {
		if value.ID == id {
			value.ChannelIDs = slices.Clone(value.ChannelIDs)
			return value, nil
		}
	}
	return core.Collection{}, fmt.Errorf("save routing catalog: collection %s missing from saved snapshot", id)
}

// ListChannels 从 Store 最新快照读取 user-owned Channel，并按 ID 稳定排序。
func (service Service) ListChannels(ctx context.Context) ([]core.Channel, error) {
	if err := service.requireStore(); err != nil {
		return nil, err
	}
	routing, err := service.Store.LoadRoutingCatalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("load routing catalog: %w", err)
	}
	result := make([]core.Channel, len(routing.Channels))
	for index, channel := range routing.Channels {
		result[index] = cloneChannel(channel)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

// GetChannel 从 Store 最新快照读取一个 user-owned Channel。
func (service Service) GetChannel(ctx context.Context, id string) (core.Channel, error) {
	if err := service.requireStore(); err != nil {
		return core.Channel{}, err
	}
	id = strings.TrimSpace(id)
	routing, err := service.Store.LoadRoutingCatalog(ctx)
	if err != nil {
		return core.Channel{}, fmt.Errorf("load routing catalog: %w", err)
	}
	for _, channel := range routing.Channels {
		if channel.ID == id {
			return cloneChannel(channel), nil
		}
	}
	return core.Channel{}, fmt.Errorf("%w: channel %s", repository.ErrNotFound, id)
}

// ListEndpoints 从 Store 最新快照读取 user-owned Endpoint。
func (service Service) ListEndpoints(ctx context.Context) ([]core.EndpointProfile, error) {
	if err := service.requireStore(); err != nil {
		return nil, err
	}
	routing, err := service.Store.LoadRoutingCatalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("load routing catalog: %w", err)
	}
	result := make([]core.EndpointProfile, len(routing.Endpoints))
	for index, endpoint := range routing.Endpoints {
		endpoint.Options = cloneAnyMap(endpoint.Options)
		result[index] = endpoint
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

// GetEndpoint 从 Store 最新快照读取一个 user-owned Endpoint。
func (service Service) GetEndpoint(ctx context.Context, id string) (core.EndpointProfile, error) {
	if err := service.requireStore(); err != nil {
		return core.EndpointProfile{}, err
	}
	id = strings.TrimSpace(id)
	routing, err := service.Store.LoadRoutingCatalog(ctx)
	if err != nil {
		return core.EndpointProfile{}, fmt.Errorf("load routing catalog: %w", err)
	}
	for _, endpoint := range routing.Endpoints {
		if endpoint.ID == id {
			endpoint.Options = cloneAnyMap(endpoint.Options)
			return endpoint, nil
		}
	}
	return core.EndpointProfile{}, fmt.Errorf("%w: endpoint %s", repository.ErrNotFound, id)
}

// ListCollections 从 Store 最新快照读取 Collection，并按 ID 稳定排序。
func (service Service) ListCollections(ctx context.Context) ([]core.Collection, error) {
	if err := service.requireStore(); err != nil {
		return nil, err
	}
	routing, err := service.Store.LoadRoutingCatalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("load routing catalog: %w", err)
	}
	result := make([]core.Collection, len(routing.Collections))
	for index, collection := range routing.Collections {
		collection.ChannelIDs = slices.Clone(collection.ChannelIDs)
		result[index] = collection
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

// GetCollection 从 Store 最新快照读取一个 Collection。
func (service Service) GetCollection(ctx context.Context, id string) (core.Collection, error) {
	if err := service.requireStore(); err != nil {
		return core.Collection{}, err
	}
	id = strings.TrimSpace(id)
	routing, err := service.Store.LoadRoutingCatalog(ctx)
	if err != nil {
		return core.Collection{}, fmt.Errorf("load routing catalog: %w", err)
	}
	for _, collection := range routing.Collections {
		if collection.ID == id {
			collection.ChannelIDs = slices.Clone(collection.ChannelIDs)
			return collection, nil
		}
	}
	return core.Collection{}, fmt.Errorf("%w: collection %s", repository.ErrNotFound, id)
}

// GetEgressProfileSummary 只返回安全摘要，不回显代理地址或代理凭据值。
func (service Service) GetEgressProfileSummary(ctx context.Context, id string) (core.EgressProfileSummary, error) {
	if err := service.requireStore(); err != nil {
		return core.EgressProfileSummary{}, err
	}
	id = strings.TrimSpace(id)
	routing, err := service.Store.LoadRoutingCatalog(ctx)
	if err != nil {
		return core.EgressProfileSummary{}, fmt.Errorf("load routing catalog: %w", err)
	}
	for _, profile := range routing.EgressProfiles {
		if profile.ID == id {
			return summarizeEgressProfile(profile), nil
		}
	}
	return core.EgressProfileSummary{}, fmt.Errorf("%w: egress profile %s", repository.ErrNotFound, id)
}

// GetCredentialDetail 默认只给掩码；includeValue 只回显持久 API Key/Token。
// chrome_cookie 即使面对错误 Store 实现也不会通过本方法返回值。
func (service Service) GetCredentialDetail(ctx context.Context, id string, includeValue bool) (core.CredentialDetail, error) {
	store, err := service.credentialStore()
	if err != nil {
		return core.CredentialDetail{}, err
	}
	id = strings.TrimSpace(id)
	credential, err := store.GetCredential(ctx, id)
	if err != nil {
		return core.CredentialDetail{}, fmt.Errorf("get credential: %w", err)
	}
	summary := summarizeCredential(credential)
	detail := core.CredentialDetail{
		ID: summary.ID, Provider: summary.Provider, AuthKind: summary.AuthKind, Label: summary.Label,
		HasValue: summary.HasValue, ValueMasked: summary.ValueMasked, Enabled: summary.Enabled, Revision: summary.Revision,
	}
	if includeValue && credential.AuthKind != "chrome_cookie" && credential.Value != nil {
		value := *credential.Value
		detail.Value = &value
	}
	return detail, nil
}

// RevokeCredential 清空持久值并禁用记录，但保留 ID 供依赖 Channel 明确
// 报告 credential blocked；revision CAS 防止覆盖刚更新的凭据。
func (service Service) RevokeCredential(ctx context.Context, id string, expectedRevision int64) (core.CredentialSummary, error) {
	store, err := service.credentialStore()
	if err != nil {
		return core.CredentialSummary{}, err
	}
	id = strings.TrimSpace(id)
	if err := validateResourceID(id); err != nil || expectedRevision < 1 {
		return core.CredentialSummary{}, fmt.Errorf("%w: credential id or expected revision is invalid", repository.ErrInvalidCredential)
	}
	credential, err := store.UpdateCredential(ctx, repository.UpdateCredential{
		ID: id, ExpectedRevision: expectedRevision, Value: nil, Enabled: false, UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		return core.CredentialSummary{}, fmt.Errorf("revoke credential: %w", err)
	}
	return summarizeCredential(credential), nil
}

type credentialDeleteStore interface {
	DeleteCredential(context.Context, repository.DeleteCredential) error
}

type semanticViewStore interface {
	ListViews(context.Context) ([]core.View, error)
}

// DeleteCredential 拒绝删除仍被 Channel 或 Egress 引用的记录；Store 会在
// 同一事务推进 routing revision，使并发新增引用的一方 CAS 失败。撤销应
// 使用 RevokeCredential；View 间接引用由 Subscription/API 层检查。
func (service Service) DeleteCredential(ctx context.Context, id string, expectedRevision int64) error {
	if err := service.requireStore(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if err := validateResourceID(id); err != nil || expectedRevision < 1 {
		return fmt.Errorf("%w: credential id or expected revision is invalid", repository.ErrInvalidCredential)
	}
	routing, err := service.Store.LoadRoutingCatalog(ctx)
	if err != nil {
		return fmt.Errorf("load routing catalog: %w", err)
	}
	for _, channel := range routing.Channels {
		if channel.CredentialID == id {
			return fmt.Errorf("%w: credential %s is referenced by channel %s", repository.ErrInUse, id, channel.ID)
		}
	}
	for _, profile := range routing.EgressProfiles {
		if profile.CredentialID == id {
			return fmt.Errorf("%w: credential %s is referenced by egress profile %s", repository.ErrInUse, id, profile.ID)
		}
	}
	for _, profile := range routing.SemanticProfiles {
		if profile.CredentialID == id {
			return fmt.Errorf("%w: credential %s is referenced by semantic profile %s", repository.ErrInUse, id, profile.ID)
		}
	}
	store, ok := service.Store.(credentialDeleteStore)
	if !ok {
		return errors.New("configured store does not support credential deletion")
	}
	if err := store.DeleteCredential(ctx, repository.DeleteCredential{
		ID: id, ExpectedRevision: expectedRevision, ExpectedRoutingRevision: routing.Revision,
	}); err != nil {
		return fmt.Errorf("delete credential: %w", err)
	}
	return nil
}

// DeleteChannel 只删除 user routing snapshot 中且未被 fallback/Collection
// 引用的 Channel。View scope 引用必须由调用它的 Subscription/API 层先检查。
func (service Service) DeleteChannel(ctx context.Context, id string, expectedRevision int64) error {
	if err := service.requireStore(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if err := validateResourceID(id); err != nil || expectedRevision < 1 {
		return fmt.Errorf("%w: channel id or expected revision is invalid", ErrInvalidChannel)
	}
	routing, err := service.Store.LoadRoutingCatalog(ctx)
	if err != nil {
		return fmt.Errorf("load routing catalog: %w", err)
	}
	working := cloneRoutingCatalog(routing)
	index, exists := indexChannels(working.Channels)[id]
	if !exists {
		return fmt.Errorf("%w: channel %s", repository.ErrNotFound, id)
	}
	if working.Channels[index].Revision != expectedRevision {
		return fmt.Errorf("%w: channel %s revision is %d, expected %d", repository.ErrConflict, id, working.Channels[index].Revision, expectedRevision)
	}
	for _, channel := range working.Channels {
		if slices.Contains(channel.FallbackChannelIDs, id) {
			return fmt.Errorf("%w: channel %s is a fallback of channel %s", repository.ErrInUse, id, channel.ID)
		}
	}
	for _, collection := range working.Collections {
		if slices.Contains(collection.ChannelIDs, id) {
			return fmt.Errorf("%w: channel %s belongs to collection %s", repository.ErrInUse, id, collection.ID)
		}
	}
	working.Channels = slices.Delete(working.Channels, index, index+1)
	_, err = service.saveRoutingCatalog(ctx, routing.Revision, working)
	return err
}

// DeleteEndpoint 只删除 user routing snapshot 中未被 Channel 引用的资源。
// View 引用检查属于持有 View Repository 的 Subscription/API 层。
func (service Service) DeleteEndpoint(ctx context.Context, id string, expectedRevision int64) error {
	if err := service.requireStore(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if err := validateResourceID(id); err != nil || expectedRevision < 1 {
		return fmt.Errorf("%w: endpoint id or expected revision is invalid", ErrInvalidEndpoint)
	}
	routing, err := service.Store.LoadRoutingCatalog(ctx)
	if err != nil {
		return fmt.Errorf("load routing catalog: %w", err)
	}
	working := cloneRoutingCatalog(routing)
	index, exists := indexEndpoints(working.Endpoints)[id]
	if !exists {
		return fmt.Errorf("%w: endpoint %s", repository.ErrNotFound, id)
	}
	if working.Endpoints[index].Revision != expectedRevision {
		return fmt.Errorf("%w: endpoint %s revision is %d, expected %d", repository.ErrConflict, id, working.Endpoints[index].Revision, expectedRevision)
	}
	for _, channel := range working.Channels {
		if channel.EndpointProfileID == id {
			return fmt.Errorf("%w: endpoint %s is referenced by channel %s", repository.ErrInUse, id, channel.ID)
		}
	}
	for _, profile := range working.SemanticProfiles {
		if profile.EndpointProfileID == id {
			return fmt.Errorf("%w: endpoint %s is referenced by semantic profile %s", repository.ErrInUse, id, profile.ID)
		}
	}
	working.Endpoints = slices.Delete(working.Endpoints, index, index+1)
	_, err = service.saveRoutingCatalog(ctx, routing.Revision, working)
	return err
}

// DeleteSemanticProfile 不级联修改 View、Endpoint、Credential 或 cache。
// 仍被持久 View Operation 引用时必须由调用方先改写或删除 View。
func (service Service) DeleteSemanticProfile(ctx context.Context, id string, expectedRevision int64) error {
	if err := service.requireStore(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if err := validateResourceID(id); err != nil || expectedRevision < 1 {
		return fmt.Errorf("%w: profile id or expected revision is invalid", ErrInvalidSemantic)
	}
	routing, err := service.Store.LoadRoutingCatalog(ctx)
	if err != nil {
		return fmt.Errorf("load routing catalog: %w", err)
	}
	working := cloneRoutingCatalog(routing)
	index, exists := indexSemanticProfiles(working.SemanticProfiles)[id]
	if !exists {
		return fmt.Errorf("%w: semantic profile %s", repository.ErrNotFound, id)
	}
	if working.SemanticProfiles[index].Revision != expectedRevision {
		return fmt.Errorf("%w: semantic profile %s revision is %d, expected %d", repository.ErrConflict, id, working.SemanticProfiles[index].Revision, expectedRevision)
	}
	store, ok := service.Store.(semanticViewStore)
	if !ok {
		return errors.New("configured store does not support semantic profile reference checks")
	}
	views, err := store.ListViews(ctx)
	if err != nil {
		return fmt.Errorf("list views: %w", err)
	}
	for _, view := range views {
		if view.Operation.SemanticProfileID != nil && *view.Operation.SemanticProfileID == id {
			return fmt.Errorf("%w: semantic profile %s is referenced by view %s", repository.ErrInUse, id, view.ID)
		}
	}
	working.SemanticProfiles = slices.Delete(working.SemanticProfiles, index, index+1)
	_, err = service.saveRoutingCatalog(ctx, routing.Revision, working)
	return err
}

// DeleteEgressProfile 拒绝删除仍被 Endpoint 或 Channel 引用的出口。View 的
// 间接引用由持有 View Repository 的 Subscription/API 层先检查。
func (service Service) DeleteEgressProfile(ctx context.Context, id string, expectedRevision int64) error {
	if err := service.requireStore(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if err := validateResourceID(id); err != nil || expectedRevision < 1 {
		return fmt.Errorf("%w: egress profile id or expected revision is invalid", ErrInvalidEgress)
	}
	routing, err := service.Store.LoadRoutingCatalog(ctx)
	if err != nil {
		return fmt.Errorf("load routing catalog: %w", err)
	}
	working := cloneRoutingCatalog(routing)
	index, exists := indexEgressProfiles(working.EgressProfiles)[id]
	if !exists {
		return fmt.Errorf("%w: egress profile %s", repository.ErrNotFound, id)
	}
	if working.EgressProfiles[index].Revision != expectedRevision {
		return fmt.Errorf("%w: egress profile %s revision is %d, expected %d", repository.ErrConflict, id, working.EgressProfiles[index].Revision, expectedRevision)
	}
	for _, endpoint := range working.Endpoints {
		if endpoint.EgressProfileID == id {
			return fmt.Errorf("%w: egress profile %s is referenced by endpoint %s", repository.ErrInUse, id, endpoint.ID)
		}
	}
	for _, channel := range working.Channels {
		if channel.EgressProfileID == id {
			return fmt.Errorf("%w: egress profile %s is referenced by channel %s", repository.ErrInUse, id, channel.ID)
		}
	}
	working.EgressProfiles = slices.Delete(working.EgressProfiles, index, index+1)
	_, err = service.saveRoutingCatalog(ctx, routing.Revision, working)
	return err
}

// DeleteCollection 不级联删除 membership 或子 Collection。View 的 collection
// scope 引用必须由持有 View Repository 的 Subscription/API 层先检查。
func (service Service) DeleteCollection(ctx context.Context, id string, expectedRevision int64) error {
	if err := service.requireStore(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if err := validateResourceID(id); err != nil || expectedRevision < 1 {
		return fmt.Errorf("%w: collection id or expected revision is invalid", ErrInvalidCollection)
	}
	routing, err := service.Store.LoadRoutingCatalog(ctx)
	if err != nil {
		return fmt.Errorf("load routing catalog: %w", err)
	}
	working := cloneRoutingCatalog(routing)
	index := -1
	for candidate, collection := range working.Collections {
		if collection.ID == id {
			index = candidate
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("%w: collection %s", repository.ErrNotFound, id)
	}
	collection := working.Collections[index]
	if collection.Revision != expectedRevision {
		return fmt.Errorf("%w: collection %s revision is %d, expected %d", repository.ErrConflict, id, collection.Revision, expectedRevision)
	}
	if len(collection.ChannelIDs) > 0 {
		return fmt.Errorf("%w: collection %s still contains channels", repository.ErrInUse, id)
	}
	for _, candidate := range working.Collections {
		if candidate.ParentID == id {
			return fmt.Errorf("%w: collection %s has child %s", repository.ErrInUse, id, candidate.ID)
		}
	}
	working.Collections = slices.Delete(working.Collections, index, index+1)
	_, err = service.saveRoutingCatalog(ctx, routing.Revision, working)
	return err
}
