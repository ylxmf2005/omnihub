package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	yamlv3 "go.yaml.in/yaml/v3"

	"github.com/ylxmf2005/omnihub/internal/core"
	"sigs.k8s.io/yaml"
)

const (
	ImportedBundleFilename   = "sources.yaml"
	ImportedBundleAPIVersion = "omnihub.dev/v1alpha1"
	ImportedBundleKind       = "SourceBundle"
)

var (
	ErrInvalidBundle    = errors.New("invalid source bundle")
	ErrImportedConflict = errors.New("imported resource conflicts with builtin")
)

// catalogStore 是 Registry 装配所需的最窄 Repository 视图。完整 Store
// 仍由 repository 包定义，Loader 不依赖 Run、Snapshot 或 SQL 细节。
type catalogStore interface {
	LoadRoutingCatalog(context.Context) (core.RoutingCatalog, error)
	ListCredentials(context.Context) ([]core.Credential, error)
}

// Load 把内建静态声明、可选 YAML Bundle 和 SQLite 用户配置装配成
// Router/Doctor 共用的单个 Catalog。Bundle 不存储 Credential，密钥只从
// Repository 进入内存中的 preflight/readiness 路径。
func Load(ctx context.Context, store catalogStore, importedBundlePath string) (*Catalog, error) {
	builtin := BuiltinCatalog()
	sources := builtin.Sources()
	providers := builtin.Providers()
	templates := builtin.RouteTemplates()

	bundle, found, err := readImportedBundle(importedBundlePath)
	if err != nil {
		return nil, err
	}
	if found {
		if err := validateBundleHeader(bundle); err != nil {
			return nil, err
		}
		importedSourceValues := make([]core.Source, len(bundle.Sources))
		for index, source := range bundle.Sources {
			importedSourceValues[index] = core.Source{ID: source.ID, Tags: slices.Clone(source.Tags), Origin: "imported", Enabled: source.Enabled}
		}
		importedProviderValues := make([]core.Provider, len(bundle.Providers))
		for index, provider := range bundle.Providers {
			importedProviderValues[index] = core.Provider{ID: provider.ID, Capabilities: slices.Clone(provider.Capabilities), AllowsGlobalDiscovery: provider.AllowsGlobalDiscovery, Enabled: provider.Enabled}
		}
		importedTemplateValues := make([]core.RouteTemplate, len(bundle.RouteTemplates))
		for index, template := range bundle.RouteTemplates {
			importedTemplateValues[index] = core.RouteTemplate{
				RouteTemplateID: template.RouteTemplateID, Origin: "imported", SourceConstraint: template.SourceConstraint,
				Provider: template.Provider, Adapter: template.Adapter, Capabilities: slices.Clone(template.Capabilities), ContentLevel: template.ContentLevel,
				Pagination: template.Pagination, TimeRange: template.TimeRange, Auth: template.Auth, EndpointRequired: template.EndpointRequired,
				ParametersSchema: template.ParametersSchema, Cost: template.Cost, Trust: template.Trust, Limitations: slices.Clone(template.Limitations),
			}
		}

		// 先分别索引 imported 资源，使空 ID 和 Bundle 内部重复都在
		// 与 builtin 合并前失败；随后的显式冲突检查保护内建 ID。
		importedSources, err := index("imported source", importedSourceValues, func(value core.Source) string { return value.ID }, cloneSource)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidBundle, err)
		}
		importedProviders, err := index("imported provider", importedProviderValues, func(value core.Provider) string { return value.ID }, cloneProvider)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidBundle, err)
		}
		importedTemplates, err := index("imported route template", importedTemplateValues, func(value core.RouteTemplate) string { return value.RouteTemplateID }, cloneRouteTemplate)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidBundle, err)
		}
		if err := rejectImportedConflicts(builtin, importedSources, importedProviders, importedTemplates); err != nil {
			return nil, err
		}
		sources = append(sources, sortedValues(importedSources, func(value core.Source) string { return value.ID }, cloneSource)...)
		providers = append(providers, sortedValues(importedProviders, func(value core.Provider) string { return value.ID }, cloneProvider)...)
		templates = append(templates, sortedValues(importedTemplates, func(value core.RouteTemplate) string { return value.RouteTemplateID }, cloneRouteTemplate)...)
	}

	var routing core.RoutingCatalog
	var credentials []core.Credential
	if store != nil {
		routing, err = store.LoadRoutingCatalog(ctx)
		if err != nil {
			return nil, fmt.Errorf("load user routing catalog: %w", err)
		}
		credentials, err = store.ListCredentials(ctx)
		if err != nil {
			return nil, fmt.Errorf("load credentials: %w", err)
		}

		// OPML 与管理命令创建的 Source 属于 user-owned routing snapshot。
		// 它们不能遮蔽 builtin/imported Source；同 ID 冲突应在装配时显式失败。
		knownSources := make(map[string]bool, len(sources))
		for _, source := range sources {
			knownSources[source.ID] = true
		}
		for _, source := range routing.Sources {
			if source.Origin != "user" || knownSources[source.ID] {
				return nil, fmt.Errorf("assemble registry: %w: user source %s conflicts with an existing source", ErrInvalidCatalog, source.ID)
			}
			knownSources[source.ID] = true
			sources = append(sources, cloneSource(source))
		}
	}
	catalog, err := NewCatalog(sources, providers, templates, routing.Channels, routing.Endpoints, routing.EgressProfiles, credentials, routing.SemanticProfiles, routing.Collections, routing.Overlays)
	if err != nil {
		return nil, fmt.Errorf("assemble registry: %w", err)
	}
	return catalog, nil
}

func readImportedBundle(path string) (core.Bundle, bool, error) {
	if path == "" {
		return core.Bundle{}, false, nil
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return core.Bundle{}, false, nil
	}
	if err != nil {
		return core.Bundle{}, false, fmt.Errorf("read imported bundle %s: %w", path, err)
	}
	defer file.Close()

	encoded, err := io.ReadAll(file)
	if err != nil {
		return core.Bundle{}, false, fmt.Errorf("read imported bundle %s: %w", path, err)
	}
	documentDecoder := yamlv3.NewDecoder(bytes.NewReader(encoded))
	var firstDocument yamlv3.Node
	if err := documentDecoder.Decode(&firstDocument); err != nil {
		return core.Bundle{}, false, fmt.Errorf("%w: decode %s: %v", ErrInvalidBundle, path, err)
	}
	var trailingDocument yamlv3.Node
	if err := documentDecoder.Decode(&trailingDocument); err == nil {
		return core.Bundle{}, false, fmt.Errorf("%w: decode %s: more than one YAML document", ErrInvalidBundle, path)
	} else if !errors.Is(err, io.EOF) {
		return core.Bundle{}, false, fmt.Errorf("%w: decode %s: %v", ErrInvalidBundle, path, err)
	}
	jsonBytes, err := yaml.YAMLToJSONStrict(encoded)
	if err != nil {
		return core.Bundle{}, false, fmt.Errorf("%w: decode %s: %v", ErrInvalidBundle, path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(jsonBytes))
	decoder.DisallowUnknownFields()
	var bundle core.Bundle
	if err := decoder.Decode(&bundle); err != nil {
		return core.Bundle{}, false, fmt.Errorf("%w: decode %s: %v", ErrInvalidBundle, path, err)
	}
	if token := bytes.TrimSpace(jsonBytes); len(token) == 0 || token[0] != '{' || token[len(token)-1] != '}' {
		return core.Bundle{}, false, fmt.Errorf("%w: decode %s: bundle must contain exactly one object", ErrInvalidBundle, path)
	}
	return bundle, true, nil
}

func validateBundleHeader(bundle core.Bundle) error {
	if bundle.APIVersion != ImportedBundleAPIVersion {
		return fmt.Errorf("%w: apiVersion must be %q", ErrInvalidBundle, ImportedBundleAPIVersion)
	}
	if bundle.Kind != ImportedBundleKind {
		return fmt.Errorf("%w: kind must be %q", ErrInvalidBundle, ImportedBundleKind)
	}
	return nil
}

func rejectImportedConflicts(builtin *Catalog, sources map[string]core.Source, providers map[string]core.Provider, templates map[string]core.RouteTemplate) error {
	for _, id := range sortedKeys(sources) {
		if _, exists := builtin.sources[id]; exists {
			return fmt.Errorf("%w: source %s", ErrImportedConflict, id)
		}
	}
	for _, id := range sortedKeys(providers) {
		if _, exists := builtin.providers[id]; exists {
			return fmt.Errorf("%w: provider %s", ErrImportedConflict, id)
		}
	}
	for _, id := range sortedKeys(templates) {
		if _, exists := builtin.routeTemplates[id]; exists {
			return fmt.Errorf("%w: route template %s", ErrImportedConflict, id)
		}
	}
	return nil
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
