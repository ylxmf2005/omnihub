package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ylxmf2005/omnihub/internal/config"
	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/readiness"
	"github.com/ylxmf2005/omnihub/internal/registry"
	"github.com/ylxmf2005/omnihub/internal/router"
	"github.com/ylxmf2005/omnihub/internal/store/sqlite"
	"github.com/ylxmf2005/omnihub/internal/transport"
)

const usage = `usage:
  omnihub schema
  omnihub paths
  omnihub sources
  omnihub providers
  omnihub route-templates
  omnihub channels
  omnihub doctor --json
  omnihub plan < operation.json

plan only selects declared channels; it never executes an upstream request.
`

const (
	exitInternal  = 1
	exitParameter = 3
	exitConfig    = 4
)

type catalogOutput struct {
	SchemaVersion  string                `json:"schema_version"`
	Sources        *[]core.Source        `json:"sources,omitempty"`
	Providers      *[]core.Provider      `json:"providers,omitempty"`
	RouteTemplates *[]core.RouteTemplate `json:"route_templates,omitempty"`
	Channels       *[]core.Channel       `json:"channels,omitempty"`
}

type routePlanOutput struct {
	SchemaVersion    string      `json:"schema_version"`
	Kind             string      `json:"kind"`
	UpstreamExecuted bool        `json:"upstream_executed"`
	Routable         bool        `json:"routable"`
	Plan             router.Plan `json:"plan"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitParameter
	}

	var value any
	resultExitCode := 0
	switch args[0] {
	case "schema":
		if len(args) != 1 {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}
		artifacts, err := transport.Generate()
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: generate schema: %v\n", err)
			return exitInternal
		}
		value = artifacts

	case "paths":
		if len(args) != 1 {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}
		paths, err := resolveCLIPaths()
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: resolve paths: %v\n", err)
			return exitConfig
		}
		value = paths

	case "sources":
		if len(args) != 1 {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}
		catalog, closeCatalog, err := loadCatalog(context.Background())
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: load catalog: %v\n", err)
			return exitConfig
		}
		defer closeCatalog()
		sources := catalog.Sources()
		value = catalogOutput{SchemaVersion: core.SchemaVersion, Sources: &sources}

	case "providers":
		if len(args) != 1 {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}
		catalog, closeCatalog, err := loadCatalog(context.Background())
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: load catalog: %v\n", err)
			return exitConfig
		}
		defer closeCatalog()
		providers := catalog.Providers()
		value = catalogOutput{SchemaVersion: core.SchemaVersion, Providers: &providers}

	case "route-templates":
		if len(args) != 1 {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}
		catalog, closeCatalog, err := loadCatalog(context.Background())
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: load catalog: %v\n", err)
			return exitConfig
		}
		defer closeCatalog()
		templates := catalog.RouteTemplates()
		value = catalogOutput{SchemaVersion: core.SchemaVersion, RouteTemplates: &templates}

	case "channels":
		if len(args) != 1 {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}
		catalog, closeCatalog, err := loadCatalog(context.Background())
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: load catalog: %v\n", err)
			return exitConfig
		}
		defer closeCatalog()
		channels := catalog.Channels()
		if channels == nil {
			channels = []core.Channel{}
		}
		value = catalogOutput{SchemaVersion: core.SchemaVersion, Channels: &channels}

	case "doctor":
		if len(args) != 2 || args[1] != "--json" {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}
		catalog, closeCatalog, err := loadCatalog(context.Background())
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: load catalog: %v\n", err)
			return exitConfig
		}
		defer closeCatalog()
		value = readiness.Doctor(catalog, time.Now().UTC())

	case "plan":
		if len(args) != 1 {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}

		// Operation 从 stdin 严格读取，避免调用方拼接 argv 或遗漏未知字段。
		decoder := json.NewDecoder(stdin)
		decoder.DisallowUnknownFields()
		var operation core.Operation
		if err := decoder.Decode(&operation); err != nil {
			fmt.Fprintf(stderr, "omnihub: decode plan operation: %v\n", err)
			return exitParameter
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			if err == nil {
				err = errors.New("more than one JSON value")
			}
			fmt.Fprintf(stderr, "omnihub: decode plan operation: %v\n", err)
			return exitParameter
		}

		catalog, closeCatalog, err := loadCatalog(context.Background())
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: load catalog: %v\n", err)
			return exitConfig
		}
		defer closeCatalog()
		plan, err := router.Build(catalog, operation)
		if err != nil && !errors.Is(err, router.ErrNoRoute) {
			fmt.Fprintf(stderr, "omnihub: build route plan: %v\n", err)
			return exitParameter
		}
		// 空决策使用 [] 而不是 null，确保 Agent 和脚本可以始终按数组处理。
		if plan.Selected == nil {
			plan.Selected = []router.Decision{}
		}
		if plan.Skipped == nil {
			plan.Skipped = []router.Decision{}
		}
		value = routePlanOutput{
			SchemaVersion:    core.SchemaVersion,
			Kind:             "route_plan",
			UpstreamExecuted: false,
			Routable:         err == nil,
			Plan:             plan,
		}
		if errors.Is(err, router.ErrNoRoute) {
			// skipped 决策仍是有效诊断结果，但无可执行路由应保持非零退出码。
			fmt.Fprintf(stderr, "omnihub: build route plan: %v\n", err)
			resultExitCode = exitConfig
		}

	default:
		fmt.Fprint(stderr, usage)
		return exitParameter
	}

	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintf(stderr, "omnihub: encode result: %v\n", err)
		return exitInternal
	}
	return resultExitCode
}

func loadCatalog(ctx context.Context) (*registry.Catalog, func(), error) {
	paths, err := resolveCLIPaths()
	if err != nil {
		return nil, func() {}, fmt.Errorf("resolve paths: %w", err)
	}
	bundlePath := filepath.Join(paths.ConfigDir, registry.ImportedBundleFilename)

	// 查询和诊断命令必须保持无状态优先：本地库不存在时只合并
	// builtin/imported，不为一次只读命令创建 SQLite 或任何目录。
	info, err := os.Stat(paths.Database)
	if errors.Is(err, os.ErrNotExist) {
		catalog, loadErr := registry.Load(ctx, nil, bundlePath)
		return catalog, func() {}, loadErr
	}
	if err != nil {
		return nil, func() {}, fmt.Errorf("inspect database: %w", err)
	}
	if info.IsDir() {
		return nil, func() {}, fmt.Errorf("database path is a directory: %s", paths.Database)
	}

	store, err := openCatalogStore(ctx, paths.Database)
	if err != nil {
		return nil, func() {}, err
	}
	catalog, err := registry.Load(ctx, store, bundlePath)
	if err != nil {
		_ = store.Close()
		return nil, func() {}, err
	}
	return catalog, func() { _ = store.Close() }, nil
}

func openCatalogStore(ctx context.Context, databasePath string) (*sqlite.Store, error) {
	// Registry/Doctor/Plan 是读取入口。只接受已经完成当前 migration 的
	// SQLite v2，并以 mode=ro 打开；初始化与升级由后续写入命令负责。
	version, err := sqlite.SchemaVersion(ctx, databasePath)
	if err != nil {
		return nil, err
	}
	if version != 2 {
		return nil, fmt.Errorf("SQLite schema version %d requires initialization or migration to version 2", version)
	}
	return sqlite.OpenReadOnly(ctx, databasePath)
}

func resolveCLIPaths() (config.Paths, error) {
	paths, err := config.Resolve()
	if err != nil {
		return config.Paths{}, err
	}
	if value, exists := os.LookupEnv("OMNIHUB_CONFIG_DIR"); exists {
		if value == "" {
			return config.Paths{}, errors.New("OMNIHUB_CONFIG_DIR must not be empty")
		}
		paths.ConfigDir = value
	}
	if value, exists := os.LookupEnv("OMNIHUB_DATABASE"); exists {
		if value == "" {
			return config.Paths{}, errors.New("OMNIHUB_DATABASE must not be empty")
		}
		paths.Database = value
	}
	return paths, nil
}
