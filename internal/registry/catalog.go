package registry

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"unicode"

	"github.com/ylxmf2005/omnihub/internal/core"
)

var (
	ErrInvalidCatalog   = errors.New("invalid registry catalog")
	ErrBuiltinImmutable = errors.New("builtin route template is immutable")
)

type Catalog struct {
	sources        map[string]core.Source
	providers      map[string]core.Provider
	routeTemplates map[string]core.RouteTemplate
	channels       map[string]core.Channel
	endpoints      map[string]core.EndpointProfile
	egressProfiles map[string]core.EgressProfile
	credentials    map[string]core.Credential
	collections    map[string]core.Collection
	overlays       map[string]core.TemplateOverlay
}

func NewCatalog(sources []core.Source, providers []core.Provider, templates []core.RouteTemplate, channels []core.Channel, endpoints []core.EndpointProfile, egressProfiles []core.EgressProfile, credentials []core.Credential, collections []core.Collection, overlays []core.TemplateOverlay) (*Catalog, error) {
	sourceIndex, err := index("source", sources, func(value core.Source) string { return value.ID }, cloneSource)
	if err != nil {
		return nil, err
	}
	providerIndex, err := index("provider", providers, func(value core.Provider) string { return value.ID }, cloneProvider)
	if err != nil {
		return nil, err
	}
	templateIndex, err := index("route template", templates, func(value core.RouteTemplate) string { return value.RouteTemplateID }, cloneRouteTemplate)
	if err != nil {
		return nil, err
	}
	channelIndex, err := index("channel", channels, func(value core.Channel) string { return value.ID }, cloneChannel)
	if err != nil {
		return nil, err
	}
	endpointIndex, err := index("endpoint", endpoints, func(value core.EndpointProfile) string { return value.ID }, cloneEndpoint)
	if err != nil {
		return nil, err
	}
	egressProfileIndex, err := index("egress profile", egressProfiles, func(value core.EgressProfile) string { return value.ID }, cloneEgressProfile)
	if err != nil {
		return nil, err
	}
	credentialIndex, err := index("credential", credentials, func(value core.Credential) string { return value.ID }, cloneCredential)
	if err != nil {
		return nil, err
	}
	collectionIndex, err := index("collection", collections, func(value core.Collection) string { return value.ID }, cloneCollection)
	if err != nil {
		return nil, err
	}
	overlayIndex, err := index("template overlay", overlays, func(value core.TemplateOverlay) string { return value.RouteTemplateID }, cloneOverlay)
	if err != nil {
		return nil, err
	}

	catalog := &Catalog{
		sources: sourceIndex, providers: providerIndex, routeTemplates: templateIndex,
		channels: channelIndex, endpoints: endpointIndex, egressProfiles: egressProfileIndex, credentials: credentialIndex,
		collections: collectionIndex, overlays: overlayIndex,
	}
	if err := catalog.Validate(); err != nil {
		return nil, err
	}
	return catalog, nil
}

func (catalog *Catalog) Validate() error {
	// RouteTemplate 是静态能力声明，Provider 未注册时无法解释 Descriptor。
	// Provider 被用户禁用则仍保留 Catalog，由 Router/Doctor 表达不可用。
	for id, template := range catalog.routeTemplates {
		if template.RouteTemplateID != id {
			return fmt.Errorf("%w: route template id is inconsistent", ErrInvalidCatalog)
		}
		if template.Origin != "" && template.Origin != "builtin" && template.Origin != "imported" {
			return fmt.Errorf("%w: route template %s has invalid origin %q", ErrInvalidCatalog, id, template.Origin)
		}
		if template.Origin != "" && (strings.TrimSpace(template.Adapter) == "" || len(template.Capabilities) == 0 || strings.TrimSpace(template.ContentLevel) == "" || strings.TrimSpace(template.Pagination.Kind) == "" || strings.TrimSpace(template.TimeRange.Kind) == "" || strings.TrimSpace(template.Auth.Kind) == "" || strings.TrimSpace(template.Cost) == "" || strings.TrimSpace(template.Trust) == "") {
			return fmt.Errorf("%w: route template %s has an incomplete descriptor", ErrInvalidCatalog, id)
		}
		if !validSourceConstraint(template.SourceConstraint) {
			return fmt.Errorf("%w: route template %s has an invalid source constraint", ErrInvalidCatalog, id)
		}
		if _, ok := catalog.providers[template.Provider]; !ok {
			return fmt.Errorf("%w: route template %s references unknown provider %s", ErrInvalidCatalog, id, template.Provider)
		}
		if err := validateBrowserAuth(template.Auth); err != nil {
			return fmt.Errorf("%w: route template %s: %v", ErrInvalidCatalog, id, err)
		}
	}
	for id, endpoint := range catalog.endpoints {
		if endpoint.ID != id {
			return fmt.Errorf("%w: endpoint id is inconsistent", ErrInvalidCatalog)
		}
	}
	for id, profile := range catalog.egressProfiles {
		if profile.ID != id {
			return fmt.Errorf("%w: egress profile id is inconsistent", ErrInvalidCatalog)
		}
		if err := profile.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidCatalog, err)
		}
	}
	for id, credential := range catalog.credentials {
		if credential.ID != id {
			return fmt.Errorf("%w: credential id is inconsistent", ErrInvalidCatalog)
		}
	}
	for id, overlay := range catalog.overlays {
		if overlay.RouteTemplateID != id {
			return fmt.Errorf("%w: template overlay id is inconsistent", ErrInvalidCatalog)
		}
	}

	for id, channel := range catalog.channels {
		if channel.ID != id {
			return fmt.Errorf("%w: channel id is inconsistent", ErrInvalidCatalog)
		}
		// 可写资源的悬挂引用和历史冲突必须继续加载，让 Router/Doctor 返回
		// 精确诊断；管理写入和 ValidateForStorage 仍拒绝制造新冲突。
		template, ok := catalog.routeTemplates[channel.RouteTemplateID]
		if ok && !matchesConstraint(template.SourceConstraint, channel.Source) {
			return fmt.Errorf("%w: channel %s source %s violates template constraint", ErrInvalidCatalog, id, channel.Source)
		}
		if ok && channel.EndpointProfileID != "" {
			endpoint, ok := catalog.endpoints[channel.EndpointProfileID]
			if ok && endpoint.Provider != template.Provider {
				return fmt.Errorf("%w: channel %s has incompatible endpoint", ErrInvalidCatalog, id)
			}
		}
		if ok && channel.CredentialID != "" {
			credential, ok := catalog.credentials[channel.CredentialID]
			if ok && credential.Provider != template.Provider {
				return fmt.Errorf("%w: channel %s has incompatible credential", ErrInvalidCatalog, id)
			}
		}
		seenFallback := make(map[string]bool)
		for _, fallbackID := range channel.FallbackChannelIDs {
			fallback, ok := catalog.channels[fallbackID]
			if !ok || fallback.Source != channel.Source || fallbackID == id || seenFallback[fallbackID] {
				return fmt.Errorf("%w: channel %s has invalid fallback %s", ErrInvalidCatalog, id, fallbackID)
			}
			seenFallback[fallbackID] = true
		}
	}
	if err := catalog.validateFallbackCycles(); err != nil {
		return err
	}
	for id, collection := range catalog.collections {
		if collection.ID != id {
			return fmt.Errorf("%w: collection id is inconsistent", ErrInvalidCatalog)
		}
		seen := make(map[string]bool)
		for _, channelID := range collection.ChannelIDs {
			if _, ok := catalog.channels[channelID]; !ok || seen[channelID] {
				return fmt.Errorf("%w: collection %s has invalid channel %s", ErrInvalidCatalog, collection.ID, channelID)
			}
			seen[channelID] = true
		}
	}
	return nil
}

// validateBrowserAuth 把 Chrome 的权限和 Cookie 查询范围固定在受信任模板
// 描述符中。普通认证不能夹带这些字段，避免后续执行层把任意 URL 当授权。
func validateBrowserAuth(auth core.AuthDescriptor) error {
	if auth.Kind != "browser_cookie" {
		if auth.LoginURL != "" || auth.Browser != "" || len(auth.PermissionOrigins) != 0 || auth.CookieScope != nil {
			return errors.New("non-browser auth cannot declare browser scope")
		}
		return nil
	}
	if !auth.Required || auth.Browser != "chrome" || auth.CookieScope == nil || len(auth.PermissionOrigins) != 1 {
		return errors.New("browser_cookie requires Chrome, permission origins, and cookie scope")
	}
	loginURL, err := url.Parse(auth.LoginURL)
	if err != nil || loginURL.Scheme != "https" || loginURL.Hostname() == "" || loginURL.Host != loginURL.Hostname() || loginURL.User != nil || loginURL.Fragment != "" {
		return errors.New("browser_cookie login_url must be an exact HTTPS URL")
	}

	permissionHosts := make(map[string]bool, len(auth.PermissionOrigins))
	for _, pattern := range auth.PermissionOrigins {
		if pattern != strings.TrimSpace(pattern) || !strings.HasPrefix(pattern, "https://") || !strings.HasSuffix(pattern, "/*") {
			return errors.New("browser_cookie permission origin must be an exact HTTPS match pattern")
		}
		parsed, err := url.Parse(strings.TrimSuffix(pattern, "*"))
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.Host != parsed.Hostname() || parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil || parsed.Hostname() != strings.ToLower(parsed.Hostname()) || permissionHosts[parsed.Hostname()] {
			return errors.New("browser_cookie permission origins must be unique exact HTTPS hosts")
		}
		permissionHosts[parsed.Hostname()] = true
	}

	scope := auth.CookieScope
	scopeURL, err := url.Parse(scope.URL)
	if err != nil || scopeURL.Scheme != "https" || scopeURL.Hostname() == "" || scopeURL.Host != scopeURL.Hostname() || scopeURL.User != nil || scopeURL.RawQuery != "" || scopeURL.Fragment != "" || !permissionHosts[scopeURL.Hostname()] {
		return errors.New("browser_cookie scope URL must belong to a permission origin")
	}
	if scope.Store != "current" || len(scope.AllowedDomains) == 0 || len(scope.Names) == 0 || len(scope.Partitions) == 0 {
		return errors.New("browser_cookie scope requires domains, names, current store, and partitions")
	}
	seenDomains := make(map[string]bool, len(scope.AllowedDomains))
	for _, candidate := range scope.AllowedDomains {
		domain := strings.TrimPrefix(candidate, ".")
		if candidate != strings.TrimSpace(candidate) || domain == "" || domain != strings.ToLower(domain) || strings.ContainsAny(domain, "/:@?#") || seenDomains[candidate] || scopeURL.Hostname() != domain {
			return errors.New("browser_cookie allowed domain is outside the scope URL")
		}
		seenDomains[candidate] = true
	}
	seenNames := make(map[string]bool, len(scope.Names))
	for _, name := range scope.Names {
		if name == "" || name != strings.TrimSpace(name) || strings.ContainsFunc(name, unicode.IsControl) || seenNames[name] {
			return errors.New("browser_cookie names must be unique non-empty values")
		}
		seenNames[name] = true
	}
	seenPartitions := make(map[string]bool, len(scope.Partitions))
	for _, partition := range scope.Partitions {
		if partition != "unpartitioned" || seenPartitions[partition] {
			return errors.New("browser_cookie v1 only supports one unpartitioned scope")
		}
		seenPartitions[partition] = true
	}
	return nil
}

func validSourceConstraint(constraint core.SourceConstraint) bool {
	switch constraint.Kind {
	case "exact", "domain_pattern":
		return len(constraint.Values) > 0
	case "any_registered":
		return len(constraint.Values) == 0
	default:
		return false
	}
}

func (catalog *Catalog) validateFallbackCycles() error {
	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("%w: fallback cycle at %s", ErrInvalidCatalog, id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for _, fallback := range catalog.channels[id].FallbackChannelIDs {
			if err := visit(fallback); err != nil {
				return err
			}
		}
		delete(visiting, id)
		visited[id] = true
		return nil
	}
	for id := range catalog.channels {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func (catalog *Catalog) TemplateEnabled(id string) bool {
	if overlay, ok := catalog.overlays[id]; ok {
		return overlay.Enabled
	}
	_, ok := catalog.routeTemplates[id]
	return ok
}

// TemplateTrusted 表达 imported RouteTemplate 是否经过 user-owned overlay
// 显式提升。Builtin 声明由发布物承担信任，普通 imported 模板无需这一步；
// browser/cookie 等高权限路径由 Router/Doctor 调用该判断。
func (catalog *Catalog) TemplateTrusted(id string) bool {
	template, ok := catalog.routeTemplates[id]
	if !ok {
		return false
	}
	if template.Origin != "imported" {
		return true
	}
	overlay, ok := catalog.overlays[id]
	return ok && overlay.Trusted
}

func (catalog *Catalog) ReplaceBuiltinTemplate(core.RouteTemplate) error {
	return ErrBuiltinImmutable
}

func (catalog *Catalog) Copy() *Catalog {
	return &Catalog{
		sources: cloneIndex(catalog.sources, cloneSource), providers: cloneIndex(catalog.providers, cloneProvider),
		routeTemplates: cloneIndex(catalog.routeTemplates, cloneRouteTemplate), channels: cloneIndex(catalog.channels, cloneChannel),
		endpoints: cloneIndex(catalog.endpoints, cloneEndpoint), egressProfiles: cloneIndex(catalog.egressProfiles, cloneEgressProfile), credentials: cloneIndex(catalog.credentials, cloneCredential),
		collections: cloneIndex(catalog.collections, cloneCollection), overlays: cloneIndex(catalog.overlays, cloneOverlay),
	}
}

func (catalog *Catalog) SortedChannels() []core.Channel {
	return sortedValues(catalog.channels, func(value core.Channel) string { return value.ID }, cloneChannel)
}

func (catalog *Catalog) Sources() []core.Source {
	return sortedValues(catalog.sources, func(value core.Source) string { return value.ID }, cloneSource)
}

func (catalog *Catalog) Providers() []core.Provider {
	return sortedValues(catalog.providers, func(value core.Provider) string { return value.ID }, cloneProvider)
}

func (catalog *Catalog) Source(id string) (core.Source, bool) {
	value, ok := catalog.sources[id]
	return cloneSource(value), ok
}

func (catalog *Catalog) Provider(id string) (core.Provider, bool) {
	value, ok := catalog.providers[id]
	return cloneProvider(value), ok
}

func (catalog *Catalog) RouteTemplates() []core.RouteTemplate {
	return sortedValues(catalog.routeTemplates, func(value core.RouteTemplate) string { return value.RouteTemplateID }, cloneRouteTemplate)
}

func (catalog *Catalog) Channels() []core.Channel { return catalog.SortedChannels() }

func (catalog *Catalog) Endpoints() []core.EndpointProfile {
	return sortedValues(catalog.endpoints, func(value core.EndpointProfile) string { return value.ID }, cloneEndpoint)
}

func (catalog *Catalog) EgressProfiles() []core.EgressProfile {
	return sortedValues(catalog.egressProfiles, func(value core.EgressProfile) string { return value.ID }, cloneEgressProfile)
}

func (catalog *Catalog) Channel(id string) (core.Channel, bool) {
	value, ok := catalog.channels[id]
	return cloneChannel(value), ok
}

func (catalog *Catalog) RouteTemplate(id string) (core.RouteTemplate, bool) {
	value, ok := catalog.routeTemplates[id]
	return cloneRouteTemplate(value), ok
}

func (catalog *Catalog) Endpoint(id string) (core.EndpointProfile, bool) {
	value, ok := catalog.endpoints[id]
	return cloneEndpoint(value), ok
}

func (catalog *Catalog) EgressProfile(id string) (core.EgressProfile, bool) {
	value, ok := catalog.egressProfiles[id]
	return cloneEgressProfile(value), ok
}

func (catalog *Catalog) Credential(id string) (core.Credential, bool) {
	value, ok := catalog.credentials[id]
	return cloneCredential(value), ok
}

func (catalog *Catalog) Collection(id string) (core.Collection, bool) {
	value, ok := catalog.collections[id]
	return cloneCollection(value), ok
}

func (catalog *Catalog) WithOverlay(overlay core.TemplateOverlay) (*Catalog, error) {
	if strings.TrimSpace(overlay.RouteTemplateID) == "" {
		return nil, fmt.Errorf("%w: template overlay id is required", ErrInvalidCatalog)
	}
	if _, ok := catalog.routeTemplates[overlay.RouteTemplateID]; !ok {
		return nil, fmt.Errorf("%w: overlay references unknown template %s", ErrInvalidCatalog, overlay.RouteTemplateID)
	}
	copy := catalog.Copy()
	copy.overlays[overlay.RouteTemplateID] = cloneOverlay(overlay)
	return copy, nil
}

func (catalog *Catalog) WithCredential(credential core.Credential) *Catalog {
	copy := catalog.Copy()
	copy.credentials[credential.ID] = cloneCredential(credential)
	return copy
}

// WithEgressProfile 为一次性 Direct Feed 附加显式 direct/environment 出口。
// 调用方仍需把相同 ID 固定到临时 Channel；该方法不写入 SQLite。
func (catalog *Catalog) WithEgressProfile(profile core.EgressProfile) (*Catalog, error) {
	if err := profile.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCatalog, err)
	}
	copy := catalog.Copy()
	if _, exists := copy.egressProfiles[profile.ID]; exists {
		return nil, fmt.Errorf("%w: transient egress profile %s already exists", ErrInvalidCatalog, profile.ID)
	}
	copy.egressProfiles[profile.ID] = cloneEgressProfile(profile)
	return copy, nil
}

// WithSourceAndChannel 为一次无状态查询附加调用方显式给出的 Direct Feed。
// 它返回独立 Catalog，既不会把临时订阅写回 SQLite，也不会覆盖已注册资源。
func (catalog *Catalog) WithSourceAndChannel(source core.Source, channel core.Channel) (*Catalog, error) {
	if strings.TrimSpace(source.ID) == "" || strings.TrimSpace(channel.ID) == "" {
		return nil, fmt.Errorf("%w: transient source and channel ids are required", ErrInvalidCatalog)
	}
	if channel.Source != source.ID {
		return nil, fmt.Errorf("%w: transient channel source is inconsistent", ErrInvalidCatalog)
	}
	copy := catalog.Copy()
	if existing, ok := copy.sources[source.ID]; ok {
		if !existing.Enabled {
			return nil, fmt.Errorf("%w: source %s is disabled", ErrInvalidCatalog, source.ID)
		}
	} else {
		copy.sources[source.ID] = cloneSource(source)
	}
	if _, exists := copy.channels[channel.ID]; exists {
		return nil, fmt.Errorf("%w: transient channel %s already exists", ErrInvalidCatalog, channel.ID)
	}
	copy.channels[channel.ID] = cloneChannel(channel)
	if err := copy.Validate(); err != nil {
		return nil, err
	}
	return copy, nil
}

func matchesConstraint(constraint core.SourceConstraint, source string) bool {
	switch constraint.Kind {
	case "exact":
		return slices.Contains(constraint.Values, source)
	case "any_registered":
		return source != ""
	case "domain_pattern":
		for _, pattern := range constraint.Values {
			if source == pattern || strings.HasSuffix(source, "."+strings.TrimPrefix(pattern, "*.")) {
				return true
			}
		}
	}
	return false
}

func index[T any](kind string, values []T, id func(T) string, clone func(T) T) (map[string]T, error) {
	result := make(map[string]T, len(values))
	for _, value := range values {
		resourceID := id(value)
		if strings.TrimSpace(resourceID) == "" {
			return nil, fmt.Errorf("%w: %s id is required", ErrInvalidCatalog, kind)
		}
		if _, exists := result[resourceID]; exists {
			return nil, fmt.Errorf("%w: duplicate %s id %s", ErrInvalidCatalog, kind, resourceID)
		}
		result[resourceID] = clone(value)
	}
	return result, nil
}

func cloneIndex[T any](values map[string]T, clone func(T) T) map[string]T {
	result := make(map[string]T, len(values))
	for id, value := range values {
		result[id] = clone(value)
	}
	return result
}

func sortedValues[T any](values map[string]T, id func(T) string, clone func(T) T) []T {
	result := slices.Collect(maps.Values(values))
	slices.SortFunc(result, func(left, right T) int { return strings.Compare(id(left), id(right)) })
	for index := range result {
		result[index] = clone(result[index])
	}
	return result
}

func cloneSource(value core.Source) core.Source {
	value.Tags = slices.Clone(value.Tags)
	return value
}

func cloneProvider(value core.Provider) core.Provider {
	value.Capabilities = slices.Clone(value.Capabilities)
	return value
}

func cloneRouteTemplate(value core.RouteTemplate) core.RouteTemplate {
	value.SourceConstraint.Values = slices.Clone(value.SourceConstraint.Values)
	value.Capabilities = slices.Clone(value.Capabilities)
	value.Auth.PermissionOrigins = slices.Clone(value.Auth.PermissionOrigins)
	if value.Auth.CookieScope != nil {
		cookieScope := *value.Auth.CookieScope
		cookieScope.AllowedDomains = slices.Clone(cookieScope.AllowedDomains)
		cookieScope.Names = slices.Clone(cookieScope.Names)
		cookieScope.Partitions = slices.Clone(cookieScope.Partitions)
		value.Auth.CookieScope = &cookieScope
	}
	value.ParametersSchema = cloneStringAnyMap(value.ParametersSchema)
	value.Limitations = slices.Clone(value.Limitations)
	return value
}

func cloneChannel(value core.Channel) core.Channel {
	value.Parameters = cloneStringAnyMap(value.Parameters)
	value.FallbackChannelIDs = slices.Clone(value.FallbackChannelIDs)
	if value.FeedMetadata != nil {
		metadata := *value.FeedMetadata
		value.FeedMetadata = &metadata
	}
	return value
}

func cloneEndpoint(value core.EndpointProfile) core.EndpointProfile {
	value.Options = cloneStringAnyMap(value.Options)
	return value
}

func cloneEgressProfile(value core.EgressProfile) core.EgressProfile { return value }

func cloneCredential(value core.Credential) core.Credential {
	if value.Value != nil {
		secret := *value.Value
		value.Value = &secret
	}
	return value
}

func cloneCollection(value core.Collection) core.Collection {
	value.ChannelIDs = slices.Clone(value.ChannelIDs)
	return value
}

func cloneOverlay(value core.TemplateOverlay) core.TemplateOverlay { return value }

// Parameters 与 schema 是 JSON-shaped 数据，但具体 slice/map 类型可能来自
// Go fixture 或 JSON decoder。反射复制保留其动态类型，避免 getter 返回值与
// Catalog 共享任意深度的可变容器。
func cloneStringAnyMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	return cloneReflect(reflect.ValueOf(value)).Interface().(map[string]any)
}

func cloneReflect(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := cloneReflect(value.Elem())
		result := reflect.New(value.Type()).Elem()
		result.Set(cloned)
		return result
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			result.SetMapIndex(cloneReflect(iterator.Key()), cloneReflect(iterator.Value()))
		}
		return result
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := range value.Len() {
			result.Index(index).Set(cloneReflect(value.Index(index)))
		}
		return result
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for index := range value.Len() {
			result.Index(index).Set(cloneReflect(value.Index(index)))
		}
		return result
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.New(value.Type().Elem())
		result.Elem().Set(cloneReflect(value.Elem()))
		return result
	default:
		return value
	}
}
