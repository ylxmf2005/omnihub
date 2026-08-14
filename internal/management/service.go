// Package management 提供 Dashboard、CLI 与导入导出共用的用户路由配置写服务。
package management

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ylxmf2005/omnihub/internal/adapter"
	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/registry"
	"github.com/ylxmf2005/omnihub/internal/repository"
)

const (
	DirectFeedRouteTemplateID = "direct-feed-window"
	OPMLNamespace             = "https://omnihub.dev/ns/opml"
	MaxOPMLBytes              = 5 << 20
	MaxOPMLDepth              = 32
	MaxOPMLNodes              = 10_000
)

var (
	ErrInvalidDirectFeed   = errors.New("invalid direct feed")
	ErrInvalidOPML         = errors.New("invalid OPML")
	ErrUnsupportedTemplate = errors.New("unsupported route template")
)

// routingStore 是管理写服务所需的最窄 Repository 视图。所有写操作都先
// 读取完整快照，再用快照 revision 做一次 CAS 保存；服务层不重试并发冲突。
type routingStore interface {
	LoadRoutingCatalog(context.Context) (core.RoutingCatalog, error)
	SaveRoutingCatalog(context.Context, repository.SaveRoutingCatalog) (core.RoutingCatalog, error)
}

// Service 是 Direct Feed 管理与 OPML 导入导出的唯一写入口。Catalog 只用于
// 裁决静态 RouteTemplate/Source；可变用户状态始终以 Store 快照为准。
type Service struct {
	Store   routingStore
	Catalog *registry.Catalog
}

// ApplyDirectFeedInput 同时描述 user Source 与其 Direct Feed Channel。
// ExpectedRevision=0 表示创建；更新必须精确匹配当前 Channel revision。
// Priority=0 是 Router 支持的有效最低优先级，不被解释为“省略”。
type ApplyDirectFeedInput struct {
	SourceID           string             `json:"source_id,omitempty"`
	SourceDisplayName  string             `json:"source_display_name,omitempty"`
	SourceCanonicalURL string             `json:"source_canonical_url,omitempty"`
	ChannelID          string             `json:"channel_id,omitempty"`
	ChannelDisplayName string             `json:"channel_display_name,omitempty"`
	RouteTemplateID    string             `json:"route_template_id,omitempty"`
	URL                string             `json:"url"`
	Priority           int                `json:"priority"`
	ExpectedRevision   int64              `json:"expected_revision"`
	FeedMetadata       *core.FeedMetadata `json:"feed_metadata,omitempty"`
}

// ImportReportEntry 记录一次实体动作或一个被忽略的输入节点。Reason 不包含
// URL query value，避免错误报告反向泄露意外放入 OPML 的凭据。
type ImportReportEntry struct {
	Kind   string `json:"kind"`
	ID     string `json:"id,omitempty"`
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ImportReport 把导入结果与真实持久化 revision 一起返回。成功只表示配置已
// 经 CAS 写入，不表示 Feed 已 probe、可达或解析成功。
type ImportReport struct {
	CatalogRevision int64               `json:"catalog_revision"`
	Created         []ImportReportEntry `json:"created,omitempty"`
	Updated         []ImportReportEntry `json:"updated,omitempty"`
	Reused          []ImportReportEntry `json:"reused,omitempty"`
	Skipped         []ImportReportEntry `json:"skipped,omitempty"`
	Warnings        []ImportReportEntry `json:"warnings,omitempty"`
}

// ApplyDirectFeed 创建或更新一个用户可执行的 Direct Feed。服务只生成 url
// 参数，不接受 Endpoint/Credential 等字段，也不会在此处访问 Feed 上游。
func (service Service) ApplyDirectFeed(ctx context.Context, input ApplyDirectFeedInput) (core.Channel, error) {
	if err := service.requireStore(); err != nil {
		return core.Channel{}, err
	}
	if input.ExpectedRevision < 0 {
		return core.Channel{}, fmt.Errorf("%w: expected revision must not be negative", ErrInvalidDirectFeed)
	}
	canonicalURL, parsedFeedURL, err := normalizeHTTPURL(input.URL)
	if err != nil {
		return core.Channel{}, fmt.Errorf("%w: feed URL: %v", ErrInvalidDirectFeed, err)
	}

	routing, err := service.Store.LoadRoutingCatalog(ctx)
	if err != nil {
		return core.Channel{}, fmt.Errorf("load routing catalog: %w", err)
	}
	working := cloneRoutingCatalog(routing)
	channelIndex := indexChannels(working.Channels)

	channelID := strings.TrimSpace(input.ChannelID)
	if channelID == "" {
		channelID = stableChannelID(canonicalURL)
	}
	if err := validateResourceID(channelID); err != nil {
		return core.Channel{}, fmt.Errorf("%w: channel id: %v", ErrInvalidDirectFeed, err)
	}
	existingIndex, channelExists := channelIndex[channelID]
	var existing core.Channel
	if channelExists {
		existing = working.Channels[existingIndex]
		if existing.Revision != input.ExpectedRevision {
			return core.Channel{}, fmt.Errorf("%w: channel %s revision is %d, expected %d", repository.ErrConflict, channelID, existing.Revision, input.ExpectedRevision)
		}
		if _, templateErr := service.directFeedTemplate(existing.RouteTemplateID); templateErr != nil {
			return core.Channel{}, fmt.Errorf("%w: existing channel %s is not a Direct Feed", ErrUnsupportedTemplate, channelID)
		}
	} else if input.ExpectedRevision != 0 {
		return core.Channel{}, fmt.Errorf("%w: channel %s does not exist at revision %d", repository.ErrConflict, channelID, input.ExpectedRevision)
	}

	templateID := strings.TrimSpace(input.RouteTemplateID)
	if templateID == "" && channelExists {
		templateID = existing.RouteTemplateID
	}
	template, err := service.directFeedTemplate(templateID)
	if err != nil {
		return core.Channel{}, err
	}

	sourceID := strings.TrimSpace(input.SourceID)
	if sourceID == "" && channelExists {
		sourceID = existing.Source
	}
	if sourceID == "" {
		sourceID = stableSourceID(parsedFeedURL.Hostname())
	}
	if err := validateResourceID(sourceID); err != nil {
		return core.Channel{}, fmt.Errorf("%w: source id: %v", ErrInvalidDirectFeed, err)
	}
	if !templateAcceptsSource(template, sourceID) {
		return core.Channel{}, fmt.Errorf("%w: template %s does not accept source %s", ErrInvalidDirectFeed, template.RouteTemplateID, sourceID)
	}

	// 同一规范化 Feed URL 只能指向一个 Direct Feed Channel，避免后续 OPML
	// membership 与 Query 路由把同一订阅误当成两个独立执行单元。
	for index, candidate := range working.Channels {
		if index == existingIndex && channelExists {
			continue
		}
		candidateURL, ok := service.canonicalChannelFeedURL(candidate)
		if ok && candidateURL == canonicalURL {
			return core.Channel{}, fmt.Errorf("%w: feed URL is already owned by channel %s", repository.ErrConflict, candidate.ID)
		}
	}

	sourceCanonicalURL := strings.TrimSpace(input.SourceCanonicalURL)
	if sourceCanonicalURL != "" {
		sourceCanonicalURL, _, err = normalizeHTTPURL(sourceCanonicalURL)
		if err != nil {
			return core.Channel{}, fmt.Errorf("%w: source canonical URL: %v", ErrInvalidDirectFeed, err)
		}
	}
	metadata, err := normalizeFeedMetadata(input.FeedMetadata)
	if err != nil {
		return core.Channel{}, fmt.Errorf("%w: feed metadata: %v", ErrInvalidDirectFeed, err)
	}

	if err := service.applySource(&working, sourceID, input.SourceDisplayName, sourceCanonicalURL, parsedFeedURL); err != nil {
		return core.Channel{}, err
	}

	channel := existing
	channel.ID = channelID
	channel.Source = sourceID
	channel.RouteTemplateID = template.RouteTemplateID
	channel.EndpointProfileID = ""
	channel.CredentialID = ""
	channel.Parameters = map[string]any{"url": canonicalURL}
	channel.Priority = input.Priority
	channel.Enabled = true
	if displayName := strings.TrimSpace(input.ChannelDisplayName); displayName != "" {
		channel.DisplayName = displayName
	} else if !channelExists {
		channel.DisplayName = defaultDisplayName(parsedFeedURL, channelID)
	}
	if input.FeedMetadata != nil {
		channel.FeedMetadata = metadata
	}
	if channelExists {
		channel.Revision = existing.Revision + 1
		working.Channels[existingIndex] = channel
	} else {
		channel.Revision = 1
		working.Channels = append(working.Channels, channel)
	}

	sortRoutingResources(&working)
	saved, err := service.saveRoutingCatalog(ctx, routing.Revision, working)
	if err != nil {
		return core.Channel{}, err
	}
	for _, savedChannel := range saved.Channels {
		if savedChannel.ID == channelID {
			return cloneChannel(savedChannel), nil
		}
	}
	return core.Channel{}, fmt.Errorf("save routing catalog: channel %s missing from saved snapshot", channelID)
}

// DisableChannel 只切换 desired state，并用 Channel revision 与完整 Catalog
// revision 两层 CAS 防止覆盖同进程或其他进程刚完成的配置修改。
func (service Service) DisableChannel(ctx context.Context, id string, expectedRevision int64) (core.Channel, error) {
	if err := service.requireStore(); err != nil {
		return core.Channel{}, err
	}
	channelID := strings.TrimSpace(id)
	if err := validateResourceID(channelID); err != nil {
		return core.Channel{}, fmt.Errorf("%w: channel id: %v", ErrInvalidDirectFeed, err)
	}
	if expectedRevision < 1 {
		return core.Channel{}, fmt.Errorf("%w: expected revision must be positive", ErrInvalidDirectFeed)
	}

	routing, err := service.Store.LoadRoutingCatalog(ctx)
	if err != nil {
		return core.Channel{}, fmt.Errorf("load routing catalog: %w", err)
	}
	working := cloneRoutingCatalog(routing)
	channelIndex, exists := indexChannels(working.Channels)[channelID]
	if !exists {
		return core.Channel{}, fmt.Errorf("%w: channel %s", repository.ErrNotFound, channelID)
	}
	channel := working.Channels[channelIndex]
	if channel.Revision != expectedRevision {
		return core.Channel{}, fmt.Errorf("%w: channel %s revision is %d, expected %d", repository.ErrConflict, channelID, channel.Revision, expectedRevision)
	}
	channel.Enabled = false
	channel.Revision++
	working.Channels[channelIndex] = channel

	saved, err := service.saveRoutingCatalog(ctx, routing.Revision, working)
	if err != nil {
		return core.Channel{}, err
	}
	for _, savedChannel := range saved.Channels {
		if savedChannel.ID == channelID {
			return cloneChannel(savedChannel), nil
		}
	}
	return core.Channel{}, fmt.Errorf("save routing catalog: channel %s missing from saved snapshot", channelID)
}

// ImportOPML 导入 OPML 2.0 中的 folder 与 type=rss outline。它不会执行
// include/link、不会 discover URL，也不会把持久化成功报告成真实 Feed probe。
func (service Service) ImportOPML(ctx context.Context, reader io.Reader) (ImportReport, error) {
	var report ImportReport
	if err := service.requireStore(); err != nil {
		return report, err
	}
	if reader == nil {
		return report, fmt.Errorf("%w: reader is required", ErrInvalidOPML)
	}
	if _, err := service.directFeedTemplate(DirectFeedRouteTemplateID); err != nil {
		return report, err
	}

	outlines, err := parseOPML(reader)
	if err != nil {
		return report, err
	}
	routing, err := service.Store.LoadRoutingCatalog(ctx)
	if err != nil {
		return report, fmt.Errorf("load routing catalog: %w", err)
	}
	working := cloneRoutingCatalog(routing)
	state := newImportState(service, &working, &report)
	state.importOutlines(outlines)
	report.CatalogRevision = routing.Revision
	if !state.changed {
		return report, nil
	}

	sortRoutingResources(&working)
	saved, err := service.saveRoutingCatalog(ctx, routing.Revision, working)
	if err != nil {
		// CAS 失败时 staged action 没有成为持久事实，不能把它们继续放在
		// created/updated 中让调用方误报成功。
		report.Created = nil
		report.Updated = nil
		report.Warnings = append(report.Warnings, ImportReportEntry{Kind: "catalog", Reason: "import changes were not saved"})
		return report, err
	}
	report.CatalogRevision = saved.Revision
	return report, nil
}

// ExportOPML 导出 Direct Feed Channel 与 Collection 层级。CredentialID、
// EndpointProfileID 及其他执行配置从不进入文档；扩展 ID 只用于稳定回导。
func (service Service) ExportOPML(ctx context.Context, writer io.Writer) error {
	if err := service.requireStore(); err != nil {
		return err
	}
	if service.Catalog == nil {
		return errors.New("management catalog is required")
	}
	if writer == nil {
		return fmt.Errorf("%w: writer is required", ErrInvalidOPML)
	}
	routing, err := service.Store.LoadRoutingCatalog(ctx)
	if err != nil {
		return fmt.Errorf("load routing catalog: %w", err)
	}
	if err := routing.ValidateForStorage(); err != nil {
		return fmt.Errorf("export routing catalog: %w", err)
	}

	sources := make(map[string]core.Source, len(routing.Sources))
	for _, source := range routing.Sources {
		sources[source.ID] = source
	}
	feedChannels := make(map[string]exportFeed)
	for _, channel := range routing.Channels {
		template, templateExists := service.Catalog.RouteTemplate(channel.RouteTemplateID)
		if !templateExists || template.Adapter != "feed" {
			continue
		}
		canonicalURL, ok := service.canonicalChannelFeedURL(channel)
		if !ok {
			return fmt.Errorf("%w: channel %s must contain only a valid url parameter", ErrInvalidDirectFeed, channel.ID)
		}
		source, found := sources[channel.Source]
		if !found {
			source, _ = service.Catalog.Source(channel.Source)
		}
		feedChannels[channel.ID] = exportFeed{channel: channel, source: source, canonicalURL: canonicalURL}
	}

	children := make(map[string][]core.Collection)
	groupedChannels := make(map[string]bool)
	for _, collection := range routing.Collections {
		children[collection.ParentID] = append(children[collection.ParentID], collection)
		for _, channelID := range collection.ChannelIDs {
			if _, ok := feedChannels[channelID]; ok {
				groupedChannels[channelID] = true
			}
		}
	}
	for parentID := range children {
		sort.Slice(children[parentID], func(left, right int) bool {
			if children[parentID][left].Position != children[parentID][right].Position {
				return children[parentID][left].Position < children[parentID][right].Position
			}
			return children[parentID][left].ID < children[parentID][right].ID
		})
	}

	document := exportOPMLDocument{Version: "2.0", Namespace: OPMLNamespace, Head: exportOPMLHead{Title: "OmniHub subscriptions"}}
	var exportCollection func(core.Collection) exportOPMLOutline
	exportCollection = func(collection core.Collection) exportOPMLOutline {
		outline := exportOPMLOutline{
			Text: collection.Title, Title: collection.Title, CollectionID: collection.ID,
		}
		for _, child := range children[collection.ID] {
			outline.Outlines = append(outline.Outlines, exportCollection(child))
		}
		for _, channelID := range collection.ChannelIDs {
			if feed, ok := feedChannels[channelID]; ok {
				outline.Outlines = append(outline.Outlines, feed.outline())
			}
		}
		return outline
	}
	for _, collection := range children[""] {
		document.Body.Outlines = append(document.Body.Outlines, exportCollection(collection))
	}

	ungrouped := make([]string, 0, len(feedChannels))
	for channelID := range feedChannels {
		if !groupedChannels[channelID] {
			ungrouped = append(ungrouped, channelID)
		}
	}
	sort.Strings(ungrouped)
	for _, channelID := range ungrouped {
		document.Body.Outlines = append(document.Body.Outlines, feedChannels[channelID].outline())
	}

	// 先完整编码到内存，避免结构编码失败时给调用方留下半份 OPML。
	var encoded bytes.Buffer
	encoded.WriteString(xml.Header)
	encoder := xml.NewEncoder(&encoded)
	encoder.Indent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return fmt.Errorf("encode OPML: %w", err)
	}
	if err := encoder.Flush(); err != nil {
		return fmt.Errorf("encode OPML: %w", err)
	}
	if _, err := writer.Write(encoded.Bytes()); err != nil {
		return fmt.Errorf("write OPML: %w", err)
	}
	return nil
}

func (service Service) requireStore() error {
	if service.Store == nil {
		return errors.New("management routing store is required")
	}
	return nil
}

func (service Service) directFeedTemplate(id string) (core.RouteTemplate, error) {
	if service.Catalog == nil {
		return core.RouteTemplate{}, errors.New("management catalog is required")
	}
	templateID := strings.TrimSpace(id)
	if templateID == "" {
		templateID = DirectFeedRouteTemplateID
	}
	template, ok := service.Catalog.RouteTemplate(templateID)
	if !ok {
		return core.RouteTemplate{}, fmt.Errorf("%w: route template %s does not exist", ErrUnsupportedTemplate, templateID)
	}
	if template.Adapter != "feed" {
		return core.RouteTemplate{}, fmt.Errorf("%w: route template %s uses adapter %s, want feed", ErrUnsupportedTemplate, templateID, template.Adapter)
	}
	return template, nil
}

func (service Service) applySource(catalog *core.RoutingCatalog, id, displayName, canonicalURL string, feedURL *url.URL) error {
	for index, source := range catalog.Sources {
		if source.ID != id {
			continue
		}
		source.Origin = "user"
		source.Enabled = true
		if value := strings.TrimSpace(displayName); value != "" {
			source.DisplayName = value
		}
		if canonicalURL != "" {
			source.CanonicalURL = canonicalURL
		}
		catalog.Sources[index] = source
		return nil
	}
	if service.Catalog != nil {
		if source, ok := service.Catalog.Source(id); ok {
			if !source.Enabled {
				return fmt.Errorf("%w: source %s is disabled", ErrInvalidDirectFeed, id)
			}
			// builtin/imported Source 是只读声明，Channel 可以引用但管理服务
			// 不会用 user snapshot 遮蔽它。
			if source.Origin != "user" {
				if strings.TrimSpace(displayName) != "" || canonicalURL != "" {
					return fmt.Errorf("%w: static source %s is read-only", ErrInvalidDirectFeed, id)
				}
				return nil
			}
		}
	}
	if canonicalURL == "" {
		canonicalURL = originURL(feedURL)
	}
	if strings.TrimSpace(displayName) == "" {
		displayName = defaultDisplayName(feedURL, id)
	}
	catalog.Sources = append(catalog.Sources, core.Source{
		ID: id, DisplayName: strings.TrimSpace(displayName), CanonicalURL: canonicalURL,
		Origin: "user", Enabled: true,
	})
	return nil
}

func (service Service) saveRoutingCatalog(ctx context.Context, expectedRevision int64, catalog core.RoutingCatalog) (core.RoutingCatalog, error) {
	if err := catalog.ValidateForStorage(); err != nil {
		return core.RoutingCatalog{}, err
	}
	saved, err := service.Store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: expectedRevision, Catalog: catalog})
	if err != nil {
		return core.RoutingCatalog{}, fmt.Errorf("save routing catalog: %w", err)
	}
	return saved, nil
}

func (service Service) canonicalChannelFeedURL(channel core.Channel) (string, bool) {
	template, ok := service.Catalog.RouteTemplate(channel.RouteTemplateID)
	if !ok || template.Adapter != "feed" || len(channel.Parameters) != 1 {
		return "", false
	}
	rawURL, ok := channel.Parameters["url"].(string)
	if !ok {
		return "", false
	}
	canonicalURL, _, err := normalizeHTTPURL(rawURL)
	return canonicalURL, err == nil
}

func normalizeFeedMetadata(metadata *core.FeedMetadata) (*core.FeedMetadata, error) {
	if metadata == nil {
		return nil, nil
	}
	result := *metadata
	result.Description = strings.TrimSpace(result.Description)
	result.Language = strings.TrimSpace(result.Language)
	result.Version = strings.TrimSpace(result.Version)
	if strings.TrimSpace(result.HTMLURL) != "" {
		canonicalURL, _, err := normalizeHTTPURL(result.HTMLURL)
		if err != nil {
			return nil, fmt.Errorf("html URL: %w", err)
		}
		result.HTMLURL = canonicalURL
	}
	if result == (core.FeedMetadata{}) {
		return nil, nil
	}
	return &result, nil
}

func normalizeHTTPURL(raw string) (string, *url.URL, error) {
	// Adapter 是 URL 可执行性与 secret query 拒绝规则的唯一事实源；管理层
	// 只在其结果上收敛 host 大小写与默认端口，使持久 Channel/cache identity
	// 再次进入 Adapter.NormalizeFeedURL 时保持不变。
	normalized, err := adapter.NormalizeFeedURL(raw)
	if err != nil {
		return "", nil, err
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", nil, err
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" {
		return "", nil, errors.New("URL host is required")
	}
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(hostname, ":") {
		if port == "" {
			parsed.Host = "[" + hostname + "]"
		} else {
			parsed.Host = net.JoinHostPort(hostname, port)
		}
	} else if port == "" {
		parsed.Host = hostname
	} else {
		parsed.Host = net.JoinHostPort(hostname, port)
	}
	parsed.Fragment = ""
	return parsed.String(), parsed, nil
}

func originURL(parsed *url.URL) string {
	copy := *parsed
	copy.Path = ""
	copy.RawPath = ""
	copy.RawQuery = ""
	copy.ForceQuery = false
	copy.Fragment = ""
	return copy.String()
}

func templateAcceptsSource(template core.RouteTemplate, sourceID string) bool {
	switch template.SourceConstraint.Kind {
	case "any_registered":
		return sourceID != ""
	case "exact":
		return slices.Contains(template.SourceConstraint.Values, sourceID)
	case "domain_pattern":
		for _, pattern := range template.SourceConstraint.Values {
			if sourceID == pattern || strings.HasSuffix(sourceID, "."+strings.TrimPrefix(pattern, "*.")) {
				return true
			}
		}
	}
	return false
}

func stableSourceID(host string) string {
	normalizedHost := strings.ToLower(strings.TrimSpace(host))
	part := sanitizeIDPart(normalizedHost)
	if part == "" {
		part = "feed"
	}
	digest := sha256.Sum256([]byte(normalizedHost))
	return fmt.Sprintf("source_%s_%x", part, digest[:4])
}

func stableChannelID(canonicalURL string) string {
	digest := sha256.Sum256([]byte(canonicalURL))
	return fmt.Sprintf("channel_%x", digest[:12])
}

func stableCollectionID(path []string) string {
	digest := sha256.Sum256([]byte(strings.Join(path, "\x00")))
	return fmt.Sprintf("collection_%x", digest[:12])
}

func sanitizeIDPart(value string) string {
	var result strings.Builder
	lastSeparator := false
	for _, character := range value {
		isLetter := character >= 'a' && character <= 'z'
		isDigit := character >= '0' && character <= '9'
		if isLetter || isDigit {
			result.WriteRune(character)
			lastSeparator = false
		} else if !lastSeparator && result.Len() > 0 {
			result.WriteByte('_')
			lastSeparator = true
		}
		if result.Len() >= 40 {
			break
		}
	}
	return strings.Trim(result.String(), "_")
}

func validateResourceID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("id is required")
	}
	if len(id) > 256 || !utf8.ValidString(id) {
		return errors.New("id must be valid UTF-8 and at most 256 bytes")
	}
	if id != strings.TrimSpace(id) || strings.IndexFunc(id, unicode.IsControl) >= 0 {
		return errors.New("id must not contain surrounding whitespace or control characters")
	}
	return nil
}

func defaultDisplayName(parsed *url.URL, fallback string) string {
	if host := strings.TrimSpace(parsed.Hostname()); host != "" {
		return host
	}
	return fallback
}

func indexChannels(channels []core.Channel) map[string]int {
	result := make(map[string]int, len(channels))
	for index, channel := range channels {
		result[channel.ID] = index
	}
	return result
}

func cloneRoutingCatalog(value core.RoutingCatalog) core.RoutingCatalog {
	result := value
	result.Sources = slices.Clone(value.Sources)
	result.Endpoints = slices.Clone(value.Endpoints)
	for index := range result.Endpoints {
		result.Endpoints[index].Options = cloneAnyMap(result.Endpoints[index].Options)
	}
	result.Channels = make([]core.Channel, len(value.Channels))
	for index, channel := range value.Channels {
		result.Channels[index] = cloneChannel(channel)
	}
	result.Collections = make([]core.Collection, len(value.Collections))
	for index, collection := range value.Collections {
		collection.ChannelIDs = slices.Clone(collection.ChannelIDs)
		result.Collections[index] = collection
	}
	result.Overlays = slices.Clone(value.Overlays)
	return result
}

func cloneChannel(value core.Channel) core.Channel {
	value.Parameters = cloneAnyMap(value.Parameters)
	value.FallbackChannelIDs = slices.Clone(value.FallbackChannelIDs)
	if value.FeedMetadata != nil {
		metadata := *value.FeedMetadata
		value.FeedMetadata = &metadata
	}
	return value
}

func cloneAnyMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func sortRoutingResources(catalog *core.RoutingCatalog) {
	sort.Slice(catalog.Sources, func(left, right int) bool { return catalog.Sources[left].ID < catalog.Sources[right].ID })
	sort.Slice(catalog.Channels, func(left, right int) bool { return catalog.Channels[left].ID < catalog.Channels[right].ID })
	sort.Slice(catalog.Collections, func(left, right int) bool { return catalog.Collections[left].ID < catalog.Collections[right].ID })
}

type parsedOPMLOutline struct {
	Text         string
	Title        string
	Type         string
	XMLURL       string
	HTMLURL      string
	Description  string
	Language     string
	Version      string
	LinkURL      string
	SourceID     string
	ChannelID    string
	CollectionID string
	UnknownAttrs []string
	Children     []*parsedOPMLOutline
}

type opmlParseFrame struct {
	name         string
	outline      *parsedOPMLOutline
	insideBody   bool
	outlineDepth int
}

func parseOPML(reader io.Reader) ([]*parsedOPMLOutline, error) {
	encoded, err := io.ReadAll(io.LimitReader(reader, MaxOPMLBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read input: %v", ErrInvalidOPML, err)
	}
	if len(encoded) > MaxOPMLBytes {
		return nil, fmt.Errorf("%w: input exceeds %d bytes", ErrInvalidOPML, MaxOPMLBytes)
	}

	decoder := xml.NewDecoder(bytes.NewReader(encoded))
	var roots []*parsedOPMLOutline
	var frames []opmlParseFrame
	rootSeen, bodySeen := false, false
	outlineCount := 0
	for {
		token, tokenErr := decoder.Token()
		if errors.Is(tokenErr, io.EOF) {
			break
		}
		if tokenErr != nil {
			return nil, fmt.Errorf("%w: decode XML: %v", ErrInvalidOPML, tokenErr)
		}
		switch value := token.(type) {
		case xml.StartElement:
			name := strings.ToLower(value.Name.Local)
			if name == "outline" {
				outlineCount++
				if outlineCount > MaxOPMLNodes {
					return nil, fmt.Errorf("%w: OPML contains more than %d outline nodes", ErrInvalidOPML, MaxOPMLNodes)
				}
			}
			frame := opmlParseFrame{name: name}
			if len(frames) > 0 {
				frame.insideBody = frames[len(frames)-1].insideBody
				frame.outlineDepth = frames[len(frames)-1].outlineDepth
			}
			if len(frames) == 0 {
				if rootSeen || name != "opml" {
					return nil, fmt.Errorf("%w: document must contain one opml root", ErrInvalidOPML)
				}
				rootSeen = true
				if attributeValue(value.Attr, "", "version") != "2.0" {
					return nil, fmt.Errorf("%w: opml version must be 2.0", ErrInvalidOPML)
				}
			} else if name == "body" && len(frames) == 1 && frames[0].name == "opml" {
				if bodySeen {
					return nil, fmt.Errorf("%w: document contains more than one body", ErrInvalidOPML)
				}
				bodySeen = true
				frame.insideBody = true
			} else if name == "outline" && frame.insideBody {
				parentAcceptsOutline := frames[len(frames)-1].name == "body" || frames[len(frames)-1].outline != nil
				if parentAcceptsOutline {
					frame.outlineDepth++
					if frame.outlineDepth > MaxOPMLDepth {
						return nil, fmt.Errorf("%w: outline depth exceeds %d", ErrInvalidOPML, MaxOPMLDepth)
					}
					outline := decodeOutline(value.Attr)
					frame.outline = outline
					if frames[len(frames)-1].outline != nil {
						frames[len(frames)-1].outline.Children = append(frames[len(frames)-1].outline.Children, outline)
					} else {
						roots = append(roots, outline)
					}
				}
			}
			// XML 自身额外包含 opml/body 两层；限制未知元素继续制造极深结构。
			if len(frames)+1 > MaxOPMLDepth+2 {
				return nil, fmt.Errorf("%w: XML depth exceeds %d", ErrInvalidOPML, MaxOPMLDepth+2)
			}
			frames = append(frames, frame)
		case xml.EndElement:
			if len(frames) == 0 || frames[len(frames)-1].name != strings.ToLower(value.Name.Local) {
				return nil, fmt.Errorf("%w: inconsistent closing element", ErrInvalidOPML)
			}
			frames = frames[:len(frames)-1]
		case xml.Directive:
			return nil, fmt.Errorf("%w: XML directives are not allowed", ErrInvalidOPML)
		case xml.ProcInst:
			if strings.ToLower(value.Target) != "xml" || rootSeen {
				return nil, fmt.Errorf("%w: processing instructions are not allowed", ErrInvalidOPML)
			}
		}
	}
	if !rootSeen || !bodySeen || len(frames) != 0 {
		return nil, fmt.Errorf("%w: document must contain a complete opml/body", ErrInvalidOPML)
	}
	return roots, nil
}

func decodeOutline(attributes []xml.Attr) *parsedOPMLOutline {
	outline := &parsedOPMLOutline{}
	for _, attribute := range attributes {
		if attribute.Name.Space == "xmlns" || strings.HasPrefix(attribute.Name.Local, "xmlns:") {
			continue
		}
		if attribute.Name.Space == OPMLNamespace {
			switch strings.ToLower(attribute.Name.Local) {
			case "source_id":
				outline.SourceID = strings.TrimSpace(attribute.Value)
			case "channel_id":
				outline.ChannelID = strings.TrimSpace(attribute.Value)
			case "collection_id":
				outline.CollectionID = strings.TrimSpace(attribute.Value)
			default:
				outline.UnknownAttrs = append(outline.UnknownAttrs, qualifiedAttributeName(attribute.Name))
			}
			continue
		}
		if attribute.Name.Space != "" {
			outline.UnknownAttrs = append(outline.UnknownAttrs, qualifiedAttributeName(attribute.Name))
			continue
		}
		switch strings.ToLower(attribute.Name.Local) {
		case "text":
			outline.Text = strings.TrimSpace(attribute.Value)
		case "title":
			outline.Title = strings.TrimSpace(attribute.Value)
		case "type":
			outline.Type = strings.TrimSpace(attribute.Value)
		case "xmlurl":
			outline.XMLURL = strings.TrimSpace(attribute.Value)
		case "htmlurl":
			outline.HTMLURL = strings.TrimSpace(attribute.Value)
		case "description":
			outline.Description = strings.TrimSpace(attribute.Value)
		case "language":
			outline.Language = strings.TrimSpace(attribute.Value)
		case "version":
			outline.Version = strings.TrimSpace(attribute.Value)
		case "url":
			outline.LinkURL = strings.TrimSpace(attribute.Value)
		default:
			outline.UnknownAttrs = append(outline.UnknownAttrs, attribute.Name.Local)
		}
	}
	sort.Strings(outline.UnknownAttrs)
	return outline
}

func attributeValue(attributes []xml.Attr, namespace, local string) string {
	for _, attribute := range attributes {
		if attribute.Name.Space == namespace && strings.EqualFold(attribute.Name.Local, local) {
			return strings.TrimSpace(attribute.Value)
		}
	}
	return ""
}

func qualifiedAttributeName(name xml.Name) string {
	if name.Space == "" {
		return name.Local
	}
	return "{" + name.Space + "}" + name.Local
}

type importState struct {
	service            Service
	catalog            *core.RoutingCatalog
	report             *ImportReport
	sourceIndex        map[string]int
	channelIndex       map[string]int
	collectionIndex    map[string]int
	urlChannels        map[string]string
	touchedSources     map[string]bool
	touchedChannels    map[string]string
	touchedCollections map[string]bool
	changed            bool
}

func newImportState(service Service, catalog *core.RoutingCatalog, report *ImportReport) *importState {
	state := &importState{
		service: service, catalog: catalog, report: report,
		sourceIndex: make(map[string]int), channelIndex: make(map[string]int), collectionIndex: make(map[string]int),
		urlChannels: make(map[string]string), touchedSources: make(map[string]bool), touchedChannels: make(map[string]string), touchedCollections: make(map[string]bool),
	}
	for index, source := range catalog.Sources {
		state.sourceIndex[source.ID] = index
	}
	for index, channel := range catalog.Channels {
		state.channelIndex[channel.ID] = index
	}
	for index, collection := range catalog.Collections {
		state.collectionIndex[collection.ID] = index
	}
	channelOrder := slices.Clone(catalog.Channels)
	sort.Slice(channelOrder, func(left, right int) bool { return channelOrder[left].ID < channelOrder[right].ID })
	for _, channel := range channelOrder {
		if canonicalURL, ok := service.canonicalChannelFeedURL(channel); ok {
			if _, exists := state.urlChannels[canonicalURL]; !exists {
				state.urlChannels[canonicalURL] = channel.ID
			}
		}
	}
	return state
}

func (state *importState) importOutlines(outlines []*parsedOPMLOutline) {
	rootOccurrences := make(map[string]int)
	for position, outline := range outlines {
		kind := classifyOutline(outline)
		label := outlineLabel(outline, position)
		path := []string{pathSegment(kind, label, rootOccurrences)}
		switch kind {
		case "feed":
			state.importFeed(outline, displayPath(path))
		case "folder":
			state.importFolder(outline, "", position, path)
		default:
			state.skipOutline(outline, displayPath(path))
		}
	}
}

func (state *importState) importFolder(outline *parsedOPMLOutline, parentID string, position int, path []string) {
	pathText := displayPath(path)
	state.reportUnknownAttributes(outline, pathText)
	if outline.SourceID != "" || outline.ChannelID != "" {
		state.report.Warnings = append(state.report.Warnings, ImportReportEntry{Kind: "attribute", Path: pathText, Reason: "feed identity attributes on a folder were ignored"})
	}
	collectionID := strings.TrimSpace(outline.CollectionID)
	if collectionID == "" {
		collectionID = stableCollectionID(path)
	}
	if err := validateResourceID(collectionID); err != nil {
		state.report.Skipped = append(state.report.Skipped, ImportReportEntry{Kind: "collection", ID: collectionID, Path: pathText, Reason: "invalid collection id"})
		return
	}
	if collectionID == parentID {
		state.report.Skipped = append(state.report.Skipped, ImportReportEntry{Kind: "collection", ID: collectionID, Path: pathText, Reason: "collection cannot be its own parent"})
		return
	}
	if state.touchedCollections[collectionID] {
		state.report.Skipped = append(state.report.Skipped, ImportReportEntry{Kind: "collection", ID: collectionID, Path: pathText, Reason: "duplicate collection id collapsed"})
		return
	}
	state.touchedCollections[collectionID] = true

	channelIDs := make([]string, 0)
	seenChannels := make(map[string]bool)
	childOccurrences := make(map[string]int)
	for childPosition, child := range outline.Children {
		kind := classifyOutline(child)
		label := outlineLabel(child, childPosition)
		childPath := append(slices.Clone(path), pathSegment(kind, label, childOccurrences))
		switch kind {
		case "feed":
			channelID, ok := state.importFeed(child, displayPath(childPath))
			if !ok {
				continue
			}
			if seenChannels[channelID] {
				state.report.Skipped = append(state.report.Skipped, ImportReportEntry{Kind: "membership", ID: channelID, Path: pathText, Reason: "duplicate URL in one collection collapsed"})
				continue
			}
			seenChannels[channelID] = true
			channelIDs = append(channelIDs, channelID)
		case "folder":
			state.importFolder(child, collectionID, childPosition, childPath)
		default:
			state.skipOutline(child, displayPath(childPath))
		}
	}

	title := collectionTitle(outline)
	if title == "" {
		title = collectionID
	}
	if index, exists := state.collectionIndex[collectionID]; exists {
		existing := state.catalog.Collections[index]
		desired := existing
		desired.Title = title
		desired.ParentID = parentID
		desired.Position = position
		desired.ChannelIDs = state.mergeImportedMemberships(existing.ChannelIDs, channelIDs)
		if collectionEquivalent(existing, desired) {
			state.report.Reused = append(state.report.Reused, ImportReportEntry{Kind: "collection", ID: collectionID, Path: pathText, Reason: "collection already matches"})
			return
		}
		desired.Revision = existing.Revision + 1
		state.catalog.Collections[index] = desired
		state.changed = true
		state.report.Updated = append(state.report.Updated, ImportReportEntry{Kind: "collection", ID: collectionID, Path: pathText})
		return
	}
	collection := core.Collection{ID: collectionID, Title: title, ParentID: parentID, Position: position, ChannelIDs: channelIDs, Enabled: true, Revision: 1}
	state.catalog.Collections = append(state.catalog.Collections, collection)
	state.collectionIndex[collectionID] = len(state.catalog.Collections) - 1
	state.changed = true
	state.report.Created = append(state.report.Created, ImportReportEntry{Kind: "collection", ID: collectionID, Path: pathText})
}

func (state *importState) importFeed(outline *parsedOPMLOutline, path string) (string, bool) {
	state.reportUnknownAttributes(outline, path)
	if outline.CollectionID != "" {
		state.report.Warnings = append(state.report.Warnings, ImportReportEntry{Kind: "attribute", Path: path, Reason: "collection identity attribute on a feed was ignored"})
	}
	canonicalURL, parsedURL, err := normalizeHTTPURL(outline.XMLURL)
	if err != nil {
		state.report.Skipped = append(state.report.Skipped, ImportReportEntry{Kind: "feed", Path: path, Reason: "invalid or credential-bearing xmlUrl"})
		return "", false
	}
	explicitChannelID := strings.TrimSpace(outline.ChannelID)
	if explicitChannelID != "" {
		if err := validateResourceID(explicitChannelID); err != nil {
			state.report.Skipped = append(state.report.Skipped, ImportReportEntry{Kind: "channel", ID: explicitChannelID, Path: path, Reason: "invalid channel id"})
			return "", false
		}
	}

	channelID := explicitChannelID
	existingURLChannelID := state.urlChannels[canonicalURL]
	if channelID == "" {
		if existingURLChannelID != "" {
			channelID = existingURLChannelID
		} else {
			channelID = stableChannelID(canonicalURL)
		}
	} else if existingURLChannelID != "" && existingURLChannelID != channelID {
		state.report.Skipped = append(state.report.Skipped, ImportReportEntry{Kind: "channel", ID: channelID, Path: path, Reason: "explicit channel id conflicts with the channel already owning this URL"})
		return "", false
	}
	if importedURL, touched := state.touchedChannels[channelID]; touched {
		if importedURL == canonicalURL {
			state.report.Reused = append(state.report.Reused, ImportReportEntry{Kind: "channel", ID: channelID, Path: path, Reason: "duplicate URL reused"})
			return channelID, true
		}
		state.report.Skipped = append(state.report.Skipped, ImportReportEntry{Kind: "channel", ID: channelID, Path: path, Reason: "one channel id cannot identify multiple feed URLs"})
		return "", false
	}

	existingIndex, channelExists := state.channelIndex[channelID]
	var existing core.Channel
	if channelExists {
		existing = state.catalog.Channels[existingIndex]
		template, templateErr := state.service.directFeedTemplate(existing.RouteTemplateID)
		if templateErr != nil {
			state.report.Skipped = append(state.report.Skipped, ImportReportEntry{Kind: "channel", ID: channelID, Path: path, Reason: "channel id belongs to a non-feed template"})
			return "", false
		}
		existingCanonicalURL, ok := state.service.canonicalChannelFeedURL(existing)
		if explicitChannelID == "" && (!ok || existingCanonicalURL != canonicalURL) {
			state.report.Skipped = append(state.report.Skipped, ImportReportEntry{Kind: "channel", ID: channelID, Path: path, Reason: "generated channel id collision"})
			return "", false
		}
		_ = template
	}

	metadata, metadataWarning := metadataFromOutline(outline)
	if metadataWarning != "" {
		state.report.Warnings = append(state.report.Warnings, ImportReportEntry{Kind: "feed", ID: channelID, Path: path, Reason: metadataWarning})
	}
	desiredSourceID := strings.TrimSpace(outline.SourceID)
	if channelExists && existingURLChannelID == channelID && explicitChannelID == "" {
		// URL 复用时保留原 Channel 的 Source identity；第三方 OPML 的主机
		// 推导不能把已有用户路由偷偷迁移到另一个 Source。
		desiredSourceID = existing.Source
		if outline.SourceID != "" && outline.SourceID != existing.Source {
			state.report.Warnings = append(state.report.Warnings, ImportReportEntry{Kind: "source", ID: outline.SourceID, Path: path, Reason: "source id ignored because the URL reused an existing channel"})
		}
	}
	if desiredSourceID == "" {
		desiredSourceID = stableSourceID(parsedURL.Hostname())
	}
	if err := validateResourceID(desiredSourceID); err != nil {
		state.report.Skipped = append(state.report.Skipped, ImportReportEntry{Kind: "source", ID: desiredSourceID, Path: path, Reason: "invalid source id"})
		return "", false
	}

	templateID := DirectFeedRouteTemplateID
	if channelExists {
		templateID = existing.RouteTemplateID
	}
	template, err := state.service.directFeedTemplate(templateID)
	if err != nil || !templateAcceptsSource(template, desiredSourceID) {
		state.report.Skipped = append(state.report.Skipped, ImportReportEntry{Kind: "channel", ID: channelID, Path: path, Reason: "feed template does not accept the selected source"})
		return "", false
	}
	if !state.upsertSource(desiredSourceID, outline, parsedURL, metadata, path) {
		return "", false
	}

	channel := existing
	channel.ID = channelID
	channel.Source = desiredSourceID
	channel.RouteTemplateID = template.RouteTemplateID
	channel.EndpointProfileID = ""
	channel.CredentialID = ""
	channel.Parameters = map[string]any{"url": canonicalURL}
	if displayName := channelDisplayName(outline); displayName != "" {
		channel.DisplayName = displayName
	} else if !channelExists {
		channel.DisplayName = defaultDisplayName(parsedURL, channelID)
	}
	channel.FeedMetadata = metadata
	if channelExists {
		if channelEquivalent(existing, channel) {
			state.report.Reused = append(state.report.Reused, ImportReportEntry{Kind: "channel", ID: channelID, Path: path, Reason: "channel already matches"})
		} else {
			channel.Revision = existing.Revision + 1
			state.catalog.Channels[existingIndex] = channel
			state.changed = true
			state.report.Updated = append(state.report.Updated, ImportReportEntry{Kind: "channel", ID: channelID, Path: path})
		}
	} else {
		channel.Priority = 100
		channel.Enabled = true
		channel.Revision = 1
		state.catalog.Channels = append(state.catalog.Channels, channel)
		state.channelIndex[channelID] = len(state.catalog.Channels) - 1
		state.changed = true
		state.report.Created = append(state.report.Created, ImportReportEntry{Kind: "channel", ID: channelID, Path: path})
	}
	if channelExists {
		if oldURL, ok := state.service.canonicalChannelFeedURL(existing); ok && oldURL != canonicalURL && state.urlChannels[oldURL] == channelID {
			delete(state.urlChannels, oldURL)
		}
	}
	state.touchedChannels[channelID] = canonicalURL
	state.urlChannels[canonicalURL] = channelID
	return channelID, true
}

func (state *importState) upsertSource(id string, outline *parsedOPMLOutline, parsedURL *url.URL, metadata *core.FeedMetadata, path string) bool {
	displayName := sourceDisplayName(outline)
	canonicalURL := originURL(parsedURL)
	if metadata != nil && metadata.HTMLURL != "" {
		canonicalURL = metadata.HTMLURL
	}
	if index, exists := state.sourceIndex[id]; exists {
		existing := state.catalog.Sources[index]
		if state.touchedSources[id] {
			state.report.Reused = append(state.report.Reused, ImportReportEntry{Kind: "source", ID: id, Path: path, Reason: "source already handled in this import"})
			return true
		}
		desired := existing
		desired.Origin = "user"
		// 显式 extension ID 表达可回导的同一 Source，允许标准字段更新；
		// 主机推导 Source 可能共享多个 Feed，只在创建时采纳 Feed 标题。
		if outline.SourceID != "" {
			if displayName != "" {
				desired.DisplayName = displayName
			}
			desired.CanonicalURL = canonicalURL
		}
		if reflect.DeepEqual(existing, desired) {
			state.report.Reused = append(state.report.Reused, ImportReportEntry{Kind: "source", ID: id, Path: path, Reason: "source already matches"})
		} else {
			state.catalog.Sources[index] = desired
			state.changed = true
			state.report.Updated = append(state.report.Updated, ImportReportEntry{Kind: "source", ID: id, Path: path})
		}
		state.touchedSources[id] = true
		return true
	}
	if source, ok := state.service.Catalog.Source(id); ok && source.Origin != "user" {
		if !source.Enabled {
			state.report.Skipped = append(state.report.Skipped, ImportReportEntry{Kind: "source", ID: id, Path: path, Reason: "referenced static source is disabled"})
			return false
		}
		state.touchedSources[id] = true
		state.report.Reused = append(state.report.Reused, ImportReportEntry{Kind: "source", ID: id, Path: path, Reason: "referenced static source reused"})
		return true
	}
	if displayName == "" {
		displayName = defaultDisplayName(parsedURL, id)
	}
	source := core.Source{ID: id, DisplayName: displayName, CanonicalURL: canonicalURL, Origin: "user", Enabled: true}
	state.catalog.Sources = append(state.catalog.Sources, source)
	state.sourceIndex[id] = len(state.catalog.Sources) - 1
	state.touchedSources[id] = true
	state.changed = true
	state.report.Created = append(state.report.Created, ImportReportEntry{Kind: "source", ID: id, Path: path})
	return true
}

func (state *importState) mergeImportedMemberships(existingIDs, importedFeedIDs []string) []string {
	// 普通 import 是非破坏性 merge，不把 OPML 中缺少的订阅解释为退订。
	// 保留既有 membership 的完整顺序，再按文档顺序追加本轮新出现的 Feed。
	result := slices.Clone(existingIDs)
	seen := make(map[string]bool, len(existingIDs)+len(importedFeedIDs))
	for _, channelID := range existingIDs {
		seen[channelID] = true
	}
	for _, channelID := range importedFeedIDs {
		if !seen[channelID] {
			seen[channelID] = true
			result = append(result, channelID)
		}
	}
	return result
}

func (state *importState) skipOutline(outline *parsedOPMLOutline, path string) {
	state.reportUnknownAttributes(outline, path)
	typeName := strings.ToLower(strings.TrimSpace(outline.Type))
	reason := "non-feed leaf ignored"
	if typeName == "include" || typeName == "link" {
		reason = typeName + " outline is not executed"
	} else if typeName != "" {
		reason = "unsupported outline type " + typeName
	} else if len(outline.Children) > 0 || outline.XMLURL != "" {
		reason = "outline is neither a folder nor a type=rss feed"
	}
	state.report.Skipped = append(state.report.Skipped, ImportReportEntry{Kind: "outline", Path: path, Reason: reason})
}

func (state *importState) reportUnknownAttributes(outline *parsedOPMLOutline, path string) {
	for _, attribute := range outline.UnknownAttrs {
		state.report.Warnings = append(state.report.Warnings, ImportReportEntry{Kind: "attribute", Path: path, Reason: "ignored unknown attribute " + attribute})
	}
}

func classifyOutline(outline *parsedOPMLOutline) string {
	typeName := strings.ToLower(strings.TrimSpace(outline.Type))
	if typeName == "rss" && outline.XMLURL != "" && len(outline.Children) == 0 {
		return "feed"
	}
	if (typeName == "" || typeName == "folder") && outline.XMLURL == "" && (len(outline.Children) > 0 || outline.CollectionID != "") {
		return "folder"
	}
	return "skip"
}

func collectionTitle(outline *parsedOPMLOutline) string {
	if outline.Title != "" {
		return outline.Title
	}
	return outline.Text
}

func channelDisplayName(outline *parsedOPMLOutline) string {
	if outline.Text != "" {
		return outline.Text
	}
	return outline.Title
}

func sourceDisplayName(outline *parsedOPMLOutline) string {
	if outline.Title != "" {
		return outline.Title
	}
	return outline.Text
}

func outlineLabel(outline *parsedOPMLOutline, position int) string {
	if title := collectionTitle(outline); title != "" {
		return title
	}
	if outline.XMLURL != "" {
		return "feed"
	}
	return fmt.Sprintf("outline-%d", position+1)
}

func pathSegment(kind, label string, occurrences map[string]int) string {
	normalized := strings.TrimSpace(label)
	key := kind + "\x00" + normalized
	occurrences[key]++
	// OPML 的 title/text 属于不可信输入，可能被误填 API Key 或带凭据 URL。
	// 路径既参与稳定 Collection identity，也会进入 ImportReport，因此用完整
	// digest 保留确定性与区分度，而不把原始 label 回显给 CLI/Dashboard。
	digest := sha256.Sum256([]byte(key))
	segment := fmt.Sprintf("%s-%x", kind, digest[:])
	if occurrences[key] > 1 {
		return fmt.Sprintf("%s[%d]", segment, occurrences[key])
	}
	return segment
}

func displayPath(path []string) string {
	return "/" + strings.Join(path, "/")
}

func metadataFromOutline(outline *parsedOPMLOutline) (*core.FeedMetadata, string) {
	metadata := &core.FeedMetadata{
		Description: strings.TrimSpace(outline.Description), Language: strings.TrimSpace(outline.Language), Version: strings.TrimSpace(outline.Version),
	}
	warning := ""
	if outline.HTMLURL != "" {
		canonicalURL, _, err := normalizeHTTPURL(outline.HTMLURL)
		if err != nil {
			warning = "invalid or credential-bearing htmlUrl ignored"
		} else {
			metadata.HTMLURL = canonicalURL
		}
	}
	if *metadata == (core.FeedMetadata{}) {
		return nil, warning
	}
	return metadata, warning
}

func channelEquivalent(left, right core.Channel) bool {
	left.Revision, right.Revision = 0, 0
	return reflect.DeepEqual(left, right)
}

func collectionEquivalent(left, right core.Collection) bool {
	left.Revision, right.Revision = 0, 0
	return reflect.DeepEqual(left, right)
}

type exportFeed struct {
	channel      core.Channel
	source       core.Source
	canonicalURL string
}

func (feed exportFeed) outline() exportOPMLOutline {
	channelName := strings.TrimSpace(feed.channel.DisplayName)
	if channelName == "" {
		channelName = strings.TrimSpace(feed.source.DisplayName)
	}
	if channelName == "" {
		channelName = feed.channel.ID
	}
	sourceName := strings.TrimSpace(feed.source.DisplayName)
	if sourceName == "" {
		sourceName = channelName
	}
	outline := exportOPMLOutline{
		Text: channelName, Title: sourceName, Type: "rss", XMLURL: feed.canonicalURL,
		SourceID: feed.channel.Source, ChannelID: feed.channel.ID,
	}
	if feed.channel.FeedMetadata != nil {
		outline.HTMLURL = feed.channel.FeedMetadata.HTMLURL
		outline.Description = feed.channel.FeedMetadata.Description
		outline.Language = feed.channel.FeedMetadata.Language
		outline.Version = feed.channel.FeedMetadata.Version
	} else {
		outline.HTMLURL = feed.source.CanonicalURL
	}
	return outline
}

type exportOPMLDocument struct {
	XMLName   xml.Name       `xml:"opml"`
	Version   string         `xml:"version,attr"`
	Namespace string         `xml:"xmlns:omnihub,attr"`
	Head      exportOPMLHead `xml:"head"`
	Body      exportOPMLBody `xml:"body"`
}

type exportOPMLHead struct {
	Title string `xml:"title"`
}

type exportOPMLBody struct {
	Outlines []exportOPMLOutline `xml:"outline"`
}

type exportOPMLOutline struct {
	Text         string              `xml:"text,attr"`
	Title        string              `xml:"title,attr,omitempty"`
	Type         string              `xml:"type,attr,omitempty"`
	XMLURL       string              `xml:"xmlUrl,attr,omitempty"`
	HTMLURL      string              `xml:"htmlUrl,attr,omitempty"`
	Description  string              `xml:"description,attr,omitempty"`
	Language     string              `xml:"language,attr,omitempty"`
	Version      string              `xml:"version,attr,omitempty"`
	SourceID     string              `xml:"omnihub:source_id,attr,omitempty"`
	ChannelID    string              `xml:"omnihub:channel_id,attr,omitempty"`
	CollectionID string              `xml:"omnihub:collection_id,attr,omitempty"`
	Outlines     []exportOPMLOutline `xml:"outline"`
}
