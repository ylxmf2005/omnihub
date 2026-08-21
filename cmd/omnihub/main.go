package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/ylxmf2005/omnihub/internal/adapter"
	"github.com/ylxmf2005/omnihub/internal/browser"
	"github.com/ylxmf2005/omnihub/internal/config"
	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/health"
	"github.com/ylxmf2005/omnihub/internal/management"
	"github.com/ylxmf2005/omnihub/internal/query"
	"github.com/ylxmf2005/omnihub/internal/readiness"
	"github.com/ylxmf2005/omnihub/internal/registry"
	"github.com/ylxmf2005/omnihub/internal/repository"
	"github.com/ylxmf2005/omnihub/internal/router"
	"github.com/ylxmf2005/omnihub/internal/semantic"
	"github.com/ylxmf2005/omnihub/internal/store/sqlite"
	"github.com/ylxmf2005/omnihub/internal/subscription"
	"github.com/ylxmf2005/omnihub/internal/transport"
	"github.com/ylxmf2005/omnihub/internal/transport/dashboardassets"
	omnihubskill "github.com/ylxmf2005/omnihub/skills/omnihub"
)

const usage = `usage:
  omnihub version
  omnihub skill
  omnihub schema
  omnihub paths
  omnihub sources
  omnihub providers
  omnihub route-templates
  omnihub semantic-profiles
  omnihub semantic-profiles apply < semantic-profile.json
  omnihub semantic-profiles disable ID --revision N
  omnihub egress-profiles
  omnihub egress-profiles apply < egress-profile.json
  omnihub egress-profiles disable ID --revision N
  omnihub endpoints
  omnihub endpoints apply < endpoint.json
  omnihub endpoints apply-provider < provider-endpoint.json
  omnihub endpoints disable ID --revision N
  omnihub endpoints probe ID
  omnihub credentials
  omnihub credentials apply < credential.json
  omnihub channels
  omnihub channels apply < direct-feed.json
  omnihub channels apply-rsshub < rsshub-channel.json
  omnihub channels apply-provider < provider-channel.json
  omnihub channels disable ID --revision N
  omnihub channels probe ID [--idempotency-key KEY]
  omnihub opml import --egress-profile ID < subscriptions.opml
  omnihub opml export > subscriptions.opml
  omnihub doctor --json
  omnihub plan < operation.json
  omnihub latest < latest.json
  omnihub search < search.json
  omnihub fetch < fetch.json
  omnihub latest|search|fetch --format jsonl < operation.json
  omnihub latest --feed-url URL --source ID --egress-mode direct|environment [--limit N] [--format json|jsonl]
  omnihub search --source ID --query QUERY [--from RFC3339] [--to RFC3339] [--sort relevance|newest] [--limit N] [--format json|jsonl]
  omnihub refresh VIEW_ID --idempotency-key KEY
  omnihub maintenance prune [--apply]
  omnihub chrome-host run
  omnihub chrome-host install --extension-id ID
  omnihub chrome-host uninstall
  omnihub mcp
  omnihub serve [--listen 127.0.0.1:8787] [--dev-origin ORIGIN]

plan only selects declared channels; it never executes an upstream request.
`

// releaseVersion、releaseCommit 与 releaseDate 由发布 archive 使用
// -ldflags -X 注入；go install 则优先使用 Go build info 的真实版本事实。
var releaseVersion, releaseCommit, releaseDate string

const (
	exitInternal  = 1
	exitParameter = 3
	exitConfig    = 4
	exitFailed    = 5
)

type catalogOutput struct {
	SchemaVersion    string                       `json:"schema_version"`
	Sources          *[]core.Source               `json:"sources,omitempty"`
	Providers        *[]core.Provider             `json:"providers,omitempty"`
	RouteTemplates   *[]core.RouteTemplate        `json:"route_templates,omitempty"`
	EgressProfiles   *[]core.EgressProfileSummary `json:"egress_profiles,omitempty"`
	Endpoints        *[]core.EndpointProfile      `json:"endpoints,omitempty"`
	SemanticProfiles *[]core.SemanticProfile      `json:"semantic_profiles,omitempty"`
	Credentials      *[]core.CredentialSummary    `json:"credentials,omitempty"`
	Channels         *[]core.Channel              `json:"channels,omitempty"`
}

type routePlanOutput struct {
	SchemaVersion    string      `json:"schema_version"`
	Kind             string      `json:"kind"`
	UpstreamExecuted bool        `json:"upstream_executed"`
	Routable         bool        `json:"routable"`
	Plan             router.Plan `json:"plan"`
}

type rssHubEndpointProbeOutput struct {
	SchemaVersion     string                      `json:"schema_version"`
	EndpointProfileID string                      `json:"endpoint_profile_id"`
	Probe             adapter.RSSHubEndpointProbe `json:"probe"`
}

type channelProbeOutput struct {
	SchemaVersion string                   `json:"schema_version"`
	Run           core.Run                 `json:"run"`
	Probe         *core.ChannelProbeRecord `json:"probe,omitempty"`
}

type versionOutput struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Date     string `json:"date"`
	Modified bool   `json:"modified"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	transport.SetBuildVersion(buildVersion().Version)
	// Chrome 直接启动 manifest 中的同一二进制时，会把调用方 Extension
	// origin 作为首个参数；Native Host 不能依赖 wrapper script 才能工作。
	if len(args) > 0 && strings.HasPrefix(args[0], "chrome-extension://") {
		return runChromeHost(stdin, stdout, stderr)
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitParameter
	}

	var value any
	resultExitCode := 0
	switch args[0] {
	case "version":
		if len(args) != 1 {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}
		value = buildVersion()

	case "skill":
		if len(args) != 1 {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}
		if _, err := io.WriteString(stdout, omnihubskill.Markdown); err != nil {
			fmt.Fprintf(stderr, "omnihub: write skill: %v\n", err)
			return exitInternal
		}
		return 0

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

	case "semantic-profiles":
		if len(args) == 1 {
			service, closeService, err := openReadManagementService(context.Background())
			if err != nil {
				fmt.Fprintf(stderr, "omnihub: open semantic profile reader: %v\n", err)
				return exitConfig
			}
			defer closeService()
			profiles, listErr := service.ListSemanticProfiles(context.Background())
			if listErr != nil {
				fmt.Fprintf(stderr, "omnihub: list semantic profiles: %v\n", listErr)
				return managementExitCode(listErr)
			}
			value = catalogOutput{SchemaVersion: core.SchemaVersion, SemanticProfiles: &profiles}
			break
		}
		if len(args) < 2 || args[1] != "apply" && args[1] != "disable" {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}
		switch args[1] {
		case "apply":
			if len(args) != 2 {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
			input, decodeErr := decodeStrictJSON[management.ApplySemanticProfileInput](stdin)
			if decodeErr != nil {
				fmt.Fprintf(stderr, "omnihub: decode semantic profile: %v\n", decodeErr)
				return exitParameter
			}
			service, closeService, openErr := openManagementService(context.Background())
			if openErr != nil {
				fmt.Fprintf(stderr, "omnihub: open management service: %v\n", openErr)
				return exitConfig
			}
			defer closeService()
			profile, applyErr := service.ApplySemanticProfile(context.Background(), input)
			if applyErr != nil {
				fmt.Fprintf(stderr, "omnihub: apply semantic profile: %v\n", applyErr)
				return managementExitCode(applyErr)
			}
			value = profile
		case "disable":
			if len(args) < 3 {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
			expectedRevision, parseErr := revisionFlag("semantic-profiles disable", args[3:], stderr)
			if parseErr != nil {
				return exitParameter
			}
			service, closeService, openErr := openManagementService(context.Background())
			if openErr != nil {
				fmt.Fprintf(stderr, "omnihub: open management service: %v\n", openErr)
				return exitConfig
			}
			defer closeService()
			profile, disableErr := service.DisableSemanticProfile(context.Background(), args[2], expectedRevision)
			if disableErr != nil {
				fmt.Fprintf(stderr, "omnihub: disable semantic profile: %v\n", disableErr)
				return managementExitCode(disableErr)
			}
			value = profile
		}

	case "egress-profiles":
		if len(args) == 1 {
			service, closeService, err := openReadManagementService(context.Background())
			if err != nil {
				fmt.Fprintf(stderr, "omnihub: open egress profile reader: %v\n", err)
				return exitConfig
			}
			defer closeService()
			profiles, listErr := service.ListEgressProfileSummaries(context.Background())
			if listErr != nil {
				fmt.Fprintf(stderr, "omnihub: list egress profiles: %v\n", listErr)
				return managementExitCode(listErr)
			}
			value = catalogOutput{SchemaVersion: core.SchemaVersion, EgressProfiles: &profiles}
			break
		}
		if len(args) < 2 || args[1] != "apply" && args[1] != "disable" {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}
		switch args[1] {
		case "apply":
			if len(args) != 2 {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
			input, decodeErr := decodeStrictJSON[management.ApplyEgressProfileInput](stdin)
			if decodeErr != nil {
				fmt.Fprintf(stderr, "omnihub: decode egress profile: %v\n", decodeErr)
				return exitParameter
			}
			service, closeService, openErr := openManagementService(context.Background())
			if openErr != nil {
				fmt.Fprintf(stderr, "omnihub: open management service: %v\n", openErr)
				return exitConfig
			}
			defer closeService()
			profile, applyErr := service.ApplyEgressProfile(context.Background(), input)
			if applyErr != nil {
				fmt.Fprintf(stderr, "omnihub: apply egress profile: %v\n", applyErr)
				return managementExitCode(applyErr)
			}
			value = profile
		case "disable":
			if len(args) < 3 {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
			expectedRevision, parseErr := revisionFlag("egress-profiles disable", args[3:], stderr)
			if parseErr != nil {
				return exitParameter
			}
			service, closeService, openErr := openManagementService(context.Background())
			if openErr != nil {
				fmt.Fprintf(stderr, "omnihub: open management service: %v\n", openErr)
				return exitConfig
			}
			defer closeService()
			profile, disableErr := service.DisableEgressProfile(context.Background(), args[2], expectedRevision)
			if disableErr != nil {
				fmt.Fprintf(stderr, "omnihub: disable egress profile: %v\n", disableErr)
				return managementExitCode(disableErr)
			}
			value = profile
		}

	case "endpoints":
		if len(args) == 1 {
			catalog, closeCatalog, err := loadCatalog(context.Background())
			if err != nil {
				fmt.Fprintf(stderr, "omnihub: load catalog: %v\n", err)
				return exitConfig
			}
			defer closeCatalog()
			endpoints := catalog.Endpoints()
			if endpoints == nil {
				endpoints = []core.EndpointProfile{}
			}
			value = catalogOutput{SchemaVersion: core.SchemaVersion, Endpoints: &endpoints}
			break
		}
		if len(args) < 2 || args[1] != "apply" && args[1] != "apply-provider" && args[1] != "disable" && args[1] != "probe" {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}
		switch args[1] {
		case "apply":
			if len(args) != 2 {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
			input, decodeErr := decodeStrictJSON[management.ApplyEndpointProfileInput](stdin)
			if decodeErr != nil {
				fmt.Fprintf(stderr, "omnihub: decode RSSHub endpoint: %v\n", decodeErr)
				return exitParameter
			}
			service, closeService, openErr := openManagementService(context.Background())
			if openErr != nil {
				fmt.Fprintf(stderr, "omnihub: open management service: %v\n", openErr)
				return exitConfig
			}
			defer closeService()
			endpoint, applyErr := service.ApplyEndpointProfile(context.Background(), input)
			if applyErr != nil {
				fmt.Fprintf(stderr, "omnihub: apply RSSHub endpoint: %v\n", applyErr)
				return managementExitCode(applyErr)
			}
			value = endpoint
		case "apply-provider":
			if len(args) != 2 {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
			input, decodeErr := decodeStrictJSON[management.ApplyProviderEndpointInput](stdin)
			if decodeErr != nil {
				fmt.Fprintf(stderr, "omnihub: decode provider endpoint: %v\n", decodeErr)
				return exitParameter
			}
			service, closeService, openErr := openManagementService(context.Background())
			if openErr != nil {
				fmt.Fprintf(stderr, "omnihub: open management service: %v\n", openErr)
				return exitConfig
			}
			defer closeService()
			endpoint, applyErr := service.ApplyProviderEndpoint(context.Background(), input)
			if applyErr != nil {
				fmt.Fprintf(stderr, "omnihub: apply provider endpoint: %v\n", applyErr)
				return managementExitCode(applyErr)
			}
			value = endpoint
		case "disable":
			if len(args) < 3 {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
			expectedRevision, parseErr := revisionFlag("endpoints disable", args[3:], stderr)
			if parseErr != nil {
				return exitParameter
			}
			service, closeService, openErr := openManagementService(context.Background())
			if openErr != nil {
				fmt.Fprintf(stderr, "omnihub: open management service: %v\n", openErr)
				return exitConfig
			}
			defer closeService()
			endpoint, disableErr := service.DisableEndpointProfile(context.Background(), args[2], expectedRevision)
			if disableErr != nil {
				fmt.Fprintf(stderr, "omnihub: disable RSSHub endpoint: %v\n", disableErr)
				return managementExitCode(disableErr)
			}
			value = endpoint
		case "probe":
			if len(args) != 3 {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
			probe, code := runRSSHubEndpointProbe(args[2], stderr)
			if probe == nil {
				return code
			}
			value, resultExitCode = probe, code
		}

	case "credentials":
		if len(args) == 1 {
			service, closeService, err := openReadManagementService(context.Background())
			if err != nil {
				fmt.Fprintf(stderr, "omnihub: open credential reader: %v\n", err)
				return exitConfig
			}
			defer closeService()
			credentials, listErr := service.ListCredentialSummaries(context.Background())
			if listErr != nil {
				fmt.Fprintf(stderr, "omnihub: list credentials: %v\n", listErr)
				return managementExitCode(listErr)
			}
			value = catalogOutput{SchemaVersion: core.SchemaVersion, Credentials: &credentials}
			break
		}
		if len(args) != 2 || args[1] != "apply" {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}
		input, decodeErr := decodeStrictJSON[management.ApplyCredentialInput](stdin)
		if decodeErr != nil {
			fmt.Fprintf(stderr, "omnihub: decode credential: %v\n", decodeErr)
			return exitParameter
		}
		service, closeService, openErr := openManagementService(context.Background())
		if openErr != nil {
			fmt.Fprintf(stderr, "omnihub: open management service: %v\n", openErr)
			return exitConfig
		}
		defer closeService()
		credential, applyErr := service.ApplyCredential(context.Background(), input)
		if applyErr != nil {
			fmt.Fprintf(stderr, "omnihub: apply credential: %v\n", applyErr)
			return managementExitCode(applyErr)
		}
		value = credential

	case "channels":
		if len(args) == 1 {
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
			break
		}
		if len(args) < 2 || args[1] != "apply" && args[1] != "apply-rsshub" && args[1] != "apply-provider" && args[1] != "disable" && args[1] != "probe" {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}
		switch args[1] {
		case "apply":
			if len(args) != 2 {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
			input, decodeErr := decodeStrictJSON[management.ApplyDirectFeedInput](stdin)
			if decodeErr != nil {
				fmt.Fprintf(stderr, "omnihub: decode direct feed: %v\n", decodeErr)
				return exitParameter
			}
			service, closeService, openErr := openManagementService(context.Background())
			if openErr != nil {
				fmt.Fprintf(stderr, "omnihub: open management service: %v\n", openErr)
				return exitConfig
			}
			defer closeService()
			channel, applyErr := service.ApplyDirectFeed(context.Background(), input)
			if applyErr != nil {
				fmt.Fprintf(stderr, "omnihub: apply direct feed: %v\n", applyErr)
				return managementExitCode(applyErr)
			}
			value = channel
		case "apply-rsshub":
			if len(args) != 2 {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
			input, decodeErr := decodeStrictJSON[management.ApplyRSSHubChannelInput](stdin)
			if decodeErr != nil {
				fmt.Fprintf(stderr, "omnihub: decode RSSHub channel: %v\n", decodeErr)
				return exitParameter
			}
			service, closeService, openErr := openManagementService(context.Background())
			if openErr != nil {
				fmt.Fprintf(stderr, "omnihub: open management service: %v\n", openErr)
				return exitConfig
			}
			defer closeService()
			channel, applyErr := service.ApplyRSSHubChannel(context.Background(), input)
			if applyErr != nil {
				fmt.Fprintf(stderr, "omnihub: apply RSSHub channel: %v\n", applyErr)
				return managementExitCode(applyErr)
			}
			value = channel
		case "apply-provider":
			if len(args) != 2 {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
			input, decodeErr := decodeStrictJSON[management.ApplyProviderChannelInput](stdin)
			if decodeErr != nil {
				fmt.Fprintf(stderr, "omnihub: decode provider channel: %v\n", decodeErr)
				return exitParameter
			}
			service, closeService, openErr := openManagementService(context.Background())
			if openErr != nil {
				fmt.Fprintf(stderr, "omnihub: open management service: %v\n", openErr)
				return exitConfig
			}
			defer closeService()
			channel, applyErr := service.ApplyProviderChannel(context.Background(), input)
			if applyErr != nil {
				fmt.Fprintf(stderr, "omnihub: apply provider channel: %v\n", applyErr)
				return managementExitCode(applyErr)
			}
			value = channel
		case "disable":
			if len(args) < 3 {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
			expectedRevision, parseErr := revisionFlag("channels disable", args[3:], stderr)
			if parseErr != nil {
				return exitParameter
			}
			service, closeService, openErr := openManagementService(context.Background())
			if openErr != nil {
				fmt.Fprintf(stderr, "omnihub: open management service: %v\n", openErr)
				return exitConfig
			}
			defer closeService()
			channel, disableErr := service.DisableChannel(context.Background(), args[2], expectedRevision)
			if disableErr != nil {
				fmt.Fprintf(stderr, "omnihub: disable channel: %v\n", disableErr)
				return managementExitCode(disableErr)
			}
			value = channel
		case "probe":
			if len(args) < 3 {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
			flags := flag.NewFlagSet("channels probe", flag.ContinueOnError)
			flags.SetOutput(stderr)
			var idempotencyKey string
			flags.StringVar(&idempotencyKey, "idempotency-key", "", "stable key for this Probe request")
			if err := flags.Parse(args[3:]); err != nil || flags.NArg() != 0 {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
			keyProvided := false
			flags.Visit(func(option *flag.Flag) { keyProvided = keyProvided || option.Name == "idempotency-key" })
			if keyProvided && (idempotencyKey == "" || idempotencyKey != strings.TrimSpace(idempotencyKey) || len(idempotencyKey) > 256) {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
			probe, code := runChannelProbe(args[2], idempotencyKey, stderr)
			if probe == nil {
				return code
			}
			value, resultExitCode = probe, code
		}

	case "opml":
		if len(args) < 2 || args[1] != "import" && args[1] != "export" {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}
		var importEgressProfileID string
		if args[1] == "export" {
			if len(args) != 2 {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
		} else {
			flags := flag.NewFlagSet("opml import", flag.ContinueOnError)
			flags.SetOutput(stderr)
			flags.StringVar(&importEgressProfileID, "egress-profile", "", "saved EgressProfile for newly imported feeds")
			if err := flags.Parse(args[2:]); err != nil {
				return exitParameter
			}
			if flags.NArg() != 0 || strings.TrimSpace(importEgressProfileID) == "" {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
		}
		openService := openManagementService
		if args[1] == "export" {
			openService = openReadManagementService
		}
		service, closeService, err := openService(context.Background())
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: open management service: %v\n", err)
			return exitConfig
		}
		defer closeService()
		if args[1] == "export" {
			if err := service.ExportOPML(context.Background(), stdout); err != nil {
				fmt.Fprintf(stderr, "omnihub: export OPML: %v\n", err)
				return managementExitCode(err)
			}
			return 0
		}
		report, err := service.ImportOPML(context.Background(), stdin, importEgressProfileID)
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: import OPML: %v\n", err)
			return managementExitCode(err)
		}
		value = report

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
		operation, err := decodeStrictJSON[core.Operation](stdin)
		if err != nil {
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

	case "latest", "search", "fetch":
		operationKind := core.OperationKind(args[0])
		result, outputFormat, code := runQueryCommand(operationKind, args[1:], stdin, stderr)
		if result.RequestID == "" {
			return code
		}
		if outputFormat == "jsonl" {
			if err := transport.WriteJSONL(stdout, result); err != nil {
				fmt.Fprintf(stderr, "omnihub: encode JSONL result: %v\n", err)
				return exitInternal
			}
			return code
		}
		value = result
		resultExitCode = code

	case "refresh":
		if len(args) < 2 || strings.TrimSpace(args[1]) == "" || args[1] != strings.TrimSpace(args[1]) {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}
		flags := flag.NewFlagSet("refresh", flag.ContinueOnError)
		flags.SetOutput(stderr)
		var idempotencyKey string
		flags.StringVar(&idempotencyKey, "idempotency-key", "", "stable key for this refresh request")
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 || strings.TrimSpace(idempotencyKey) == "" {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}

		paths, err := resolveCLIPaths()
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: resolve paths: %v\n", err)
			return exitConfig
		}
		store, err := sqlite.Open(context.Background(), paths.Database)
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: open subscription store: %v\n", err)
			return exitConfig
		}
		defer store.Close()
		bundlePath := filepath.Join(paths.ConfigDir, registry.ImportedBundleFilename)
		load := func(ctx context.Context) (*registry.Catalog, error) {
			return registry.Load(ctx, store, bundlePath)
		}
		instanceID, err := core.NewRequestID()
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: create refresh worker identity: %v\n", err)
			return exitInternal
		}
		execute := func(ctx context.Context, catalog *registry.Catalog, operation core.Operation) (core.Envelope, error) {
			return executeCatalogOperationWithStore(ctx, catalog, store, operation)
		}
		service := subscription.Service{Store: store, LoadCatalog: load, Execute: execute, InstanceID: instanceID}
		run, _, err := service.CreateViewRefreshRun(context.Background(), args[1], idempotencyKey)
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: create refresh run: %v\n", err)
			switch {
			case errors.Is(err, subscription.ErrInvalidRequest):
				return exitParameter
			case errors.Is(err, repository.ErrNotFound), errors.Is(err, repository.ErrIdempotency), errors.Is(err, subscription.ErrViewDisabled):
				return exitConfig
			default:
				return exitInternal
			}
		}
		now := time.Now().UTC()
		if run.Status == core.RunQueued || run.Status == core.RunRunning && (run.LeaseExpiresAt == nil || !run.LeaseExpiresAt.After(now)) {
			run, err = service.ProcessRun(context.Background(), run.ID)
			if err != nil {
				fmt.Fprintf(stderr, "omnihub: process refresh run: %v\n", err)
				return exitInternal
			}
		}
		value = run
		if run.Status == core.RunFailed {
			resultExitCode = exitFailed
		}

	case "maintenance":
		if len(args) < 2 || args[1] != "prune" {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}
		flags := flag.NewFlagSet("maintenance prune", flag.ContinueOnError)
		flags.SetOutput(stderr)
		var apply bool
		flags.BoolVar(&apply, "apply", false, "delete expired records instead of only counting them")
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}
		paths, err := resolveCLIPaths()
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: resolve paths: %v\n", err)
			return exitConfig
		}
		store, err := sqlite.Open(context.Background(), paths.Database)
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: open maintenance store: %v\n", err)
			return exitConfig
		}
		defer store.Close()
		now := time.Now().UTC()
		result, err := store.Prune(context.Background(), repository.Prune{
			DryRun: !apply, RunFinishedBefore: now.Add(-30 * 24 * time.Hour),
			ProbeCheckedBefore:    now.Add(-30 * 24 * time.Hour),
			EmbeddingUnusedBefore: now.Add(-30 * 24 * time.Hour),
			// Tombstone 在创建时已经固化 180 天 expires_at，清理当前已到期记录即可。
			TombstoneExpiresBefore: now,
		})
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: prune retention records: %v\n", err)
			return exitInternal
		}
		value = result

	case "mcp":
		if len(args) != 1 {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}
		if err := transport.RunMCPStdio(context.Background(), executeOperation); err != nil {
			fmt.Fprintf(stderr, "omnihub: run MCP server: %v\n", err)
			return exitInternal
		}
		return 0

	case "chrome-host":
		if len(args) < 2 {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}
		switch args[1] {
		case "run":
			if len(args) != 2 {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
			return runChromeHost(stdin, stdout, stderr)
		case "install":
			flags := flag.NewFlagSet("chrome-host install", flag.ContinueOnError)
			flags.SetOutput(stderr)
			extensionID := ""
			flags.StringVar(&extensionID, "extension-id", extensionID, "one exact Chrome Extension ID")
			if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 || extensionID == "" {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
			executable, err := os.Executable()
			if err != nil {
				fmt.Fprintf(stderr, "omnihub: resolve native host executable: %v\n", err)
				return exitConfig
			}
			result, err := browser.InstallHost(executable, extensionID)
			if err != nil {
				fmt.Fprintf(stderr, "omnihub: install Chrome native host: %v\n", err)
				return exitConfig
			}
			value = result
		case "uninstall":
			if len(args) != 2 {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
			result, err := browser.UninstallHost()
			if err != nil {
				fmt.Fprintf(stderr, "omnihub: uninstall Chrome native host: %v\n", err)
				return exitConfig
			}
			value = result
		default:
			fmt.Fprint(stderr, usage)
			return exitParameter
		}

	case "serve":
		flags := flag.NewFlagSet("serve", flag.ContinueOnError)
		flags.SetOutput(stderr)
		listen := "127.0.0.1:8787"
		devOrigin := ""
		flags.StringVar(&listen, "listen", listen, "literal loopback listen address")
		flags.StringVar(&devOrigin, "dev-origin", devOrigin, "one explicit loopback Dashboard development origin")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || !loopbackListenAddress(listen) {
			fmt.Fprint(stderr, usage)
			return exitParameter
		}

		paths, err := resolveCLIPaths()
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: resolve paths: %v\n", err)
			return exitConfig
		}
		store, err := sqlite.Open(context.Background(), paths.Database)
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: open serve store: %v\n", err)
			return exitConfig
		}
		defer store.Close()
		bundlePath := filepath.Join(paths.ConfigDir, registry.ImportedBundleFilename)
		load := func(ctx context.Context) (*registry.Catalog, error) {
			return registry.Load(ctx, store, bundlePath)
		}
		catalog, err := load(context.Background())
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: load serve catalog: %v\n", err)
			return exitConfig
		}
		instanceID, err := core.NewRequestID()
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: create serve instance identity: %v\n", err)
			return exitInternal
		}
		browserClient := browser.NewClient(paths.RuntimeDir)
		executeCatalog := func(ctx context.Context, catalog *registry.Catalog, operation core.Operation) (core.Envelope, error) {
			return executeCatalogOperationWithStore(ctx, catalog, store, operation)
		}
		execute := func(ctx context.Context, operation core.Operation) (core.Envelope, error) {
			current, loadErr := load(ctx)
			if loadErr != nil {
				return core.Envelope{}, fmt.Errorf("%w: %v", transport.ErrExecutionConfiguration, loadErr)
			}
			return executeCatalog(ctx, current, operation)
		}
		managementService := &management.Service{Store: store, Catalog: catalog}
		subscriptionService := &subscription.Service{
			Store: store, LoadCatalog: load, Execute: executeCatalog, InstanceID: instanceID,
		}
		readinessReport := func(ctx context.Context) (readiness.Report, error) {
			current, loadErr := load(ctx)
			if loadErr != nil {
				return readiness.Report{}, fmt.Errorf("%w: %v", transport.ErrExecutionConfiguration, loadErr)
			}
			now := time.Now().UTC()
			// Store 的 limit 约束历史行，不按 Channel 分组；逐 Channel 读取可避免
			// 一个频繁 Probe 的来源把其他来源的有效健康事实挤出窗口。
			records := make([]core.ChannelProbeRecord, 0, len(current.Channels()))
			for _, channel := range current.SortedChannels() {
				values, listErr := store.ListProbeHealth(ctx, repository.ProbeHealthFilter{
					ChannelID: channel.ID, ActiveAt: now, Limit: 1,
				})
				if listErr != nil {
					return readiness.Report{}, listErr
				}
				records = append(records, values...)
			}
			report := readiness.FromProbeHealth(current, records, now)
			bridge := core.BrowserBridge{ID: browser.BridgeID, Browser: "chrome"}
			status, statusErr := browserClient.Status(ctx)
			if statusErr == nil {
				bridge.Connected = status.Connected
				bridge.ProfileLabel = status.ProfileLabel
				bridge.GrantedOrigins = append([]string(nil), status.GrantedOrigins...)
				bridge.LastSeenAt = status.LastSeenAt
			} else if !errors.Is(statusErr, browser.ErrBrowserUnavailable) {
				return readiness.Report{}, statusErr
			}
			return readiness.WithBrowserBridge(report, current, bridge, now), nil
		}
		probe := func(ctx context.Context, channelID, idempotencyKey string) (core.Run, error) {
			current, loadErr := load(ctx)
			if loadErr != nil {
				return core.Run{}, fmt.Errorf("%w: %v", transport.ErrExecutionConfiguration, loadErr)
			}
			service := health.Service{Store: store, Catalog: current, InstanceID: instanceID}
			run, created, createErr := service.CreateProbeRun(ctx, channelID, idempotencyKey)
			if createErr != nil {
				return core.Run{}, createErr
			}
			now := time.Now().UTC()
			if created || run.Status == core.RunQueued || run.Status == core.RunRunning && (run.LeaseExpiresAt == nil || !run.LeaseExpiresAt.After(now)) {
				go func() { _, _ = service.ProcessRun(context.Background(), run.ID) }()
			}
			return run, nil
		}
		handler, err := transport.NewDashboardHTTPHandler(transport.DashboardHTTPDependencies{
			Execute: execute, LoadCatalog: load, Management: managementService, Subscription: subscriptionService,
			Readiness: readinessReport, Probe: probe, Browser: browserClient,
			Version: buildVersion().Version, InstanceID: instanceID, DevOrigin: devOrigin,
		})
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: construct Dashboard HTTP server: %v\n", err)
			return exitParameter
		}
		server := &http.Server{Addr: listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
		fmt.Fprintf(stderr, "omnihub: serving on http://%s\n", listen)
		// go:embed 在编译期取快照，所以重新构建了 Dashboard 却没有重新 go build 时，
		// serve 会安静地继续提供旧界面。这类问题只表现为「改动没生效」，很难自查，
		// 因此在开发树里显式提示一次。
		if stale, onDisk := dashboardassets.Stale("internal/transport/dashboardassets/dist"); stale {
			fmt.Fprintf(stderr, "omnihub: warning: 磁盘上的 Dashboard 构建产物（%s）比二进制里嵌入的更新；重新运行 go build 才会生效\n", onDisk)
		}
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(stderr, "omnihub: serve: %v\n", err)
			return exitInternal
		}
		return 0

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

type directFeedFlags struct {
	feedURL    string
	source     string
	egressMode string
	query      string
	format     string
	identity   string
	from       string
	to         string
	limit      int
	deadlineMS int
}

func runQueryCommand(kind core.OperationKind, args []string, stdin io.Reader, stderr io.Writer) (core.Envelope, string, int) {
	outputFormat := "json"
	if len(args) == 2 && args[0] == "--format" {
		outputFormat, args = args[1], nil
	}
	if outputFormat != "json" && outputFormat != "jsonl" {
		fmt.Fprintf(stderr, "omnihub: unsupported output format %q\n", outputFormat)
		return core.Envelope{}, "", exitParameter
	}
	operation, transient, err := queryOperation(kind, args, stdin, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "omnihub: decode %s operation: %v\n", kind, err)
		return core.Envelope{}, "", exitParameter
	}

	var envelope core.Envelope
	if operation.SimilarityGrouping == core.SimilaritySemantic {
		// semantic 需要复用一个可写 Store 完成 cache read/write；普通查询继续
		// 走无数据库优先路径，一次性 --feed-url 始终固定为 off。
		envelope, err = executeOperation(context.Background(), operation)
	} else {
		catalog, closeCatalog, loadErr := loadCatalog(context.Background())
		if loadErr != nil {
			fmt.Fprintf(stderr, "omnihub: load catalog: %v\n", loadErr)
			return core.Envelope{}, "", exitConfig
		}
		defer closeCatalog()
		if transient != nil {
			catalog, err = catalog.WithEgressProfile(transient.egress)
			if err != nil {
				fmt.Fprintf(stderr, "omnihub: configure direct feed egress: %v\n", err)
				return core.Envelope{}, "", exitConfig
			}
			catalog, err = catalog.WithSourceAndChannel(transient.source, transient.channel)
			if err != nil {
				fmt.Fprintf(stderr, "omnihub: configure direct feed: %v\n", err)
				return core.Envelope{}, "", exitConfig
			}
			outputFormat = transient.format
		}
		envelope, err = executeCatalogOperation(context.Background(), catalog, operation)
	}
	if err != nil {
		if errors.Is(err, router.ErrNoRoute) {
			fmt.Fprintf(stderr, "omnihub: execute %s: %v\n", kind, err)
			return core.Envelope{}, "", exitConfig
		}
		fmt.Fprintf(stderr, "omnihub: execute %s: %v\n", kind, err)
		if errors.Is(err, core.ErrInvalidOperation) {
			return core.Envelope{}, "", exitParameter
		}
		if errors.Is(err, transport.ErrExecutionConfiguration) {
			return core.Envelope{}, "", exitConfig
		}
		return core.Envelope{}, "", exitInternal
	}
	if envelope.Status == core.StatusFailed {
		return envelope, outputFormat, exitFailed
	}
	return envelope, outputFormat, 0
}

func executeOperation(ctx context.Context, operation core.Operation) (core.Envelope, error) {
	if operation.SimilarityGrouping == core.SimilaritySemantic {
		paths, err := resolveCLIPaths()
		if err != nil {
			return core.Envelope{}, fmt.Errorf("%w: resolve paths: %v", transport.ErrExecutionConfiguration, err)
		}
		info, err := os.Stat(paths.Database)
		if errors.Is(err, os.ErrNotExist) {
			return core.Envelope{}, fmt.Errorf("%w: semantic grouping requires an existing configured database", transport.ErrExecutionConfiguration)
		}
		if err != nil {
			return core.Envelope{}, fmt.Errorf("%w: inspect database: %v", transport.ErrExecutionConfiguration, err)
		}
		if info.IsDir() {
			return core.Envelope{}, fmt.Errorf("%w: database path is a directory", transport.ErrExecutionConfiguration)
		}
		store, err := sqlite.Open(ctx, paths.Database)
		if err != nil {
			return core.Envelope{}, fmt.Errorf("%w: open semantic store: %v", transport.ErrExecutionConfiguration, err)
		}
		defer store.Close()
		catalog, err := registry.Load(ctx, store, filepath.Join(paths.ConfigDir, registry.ImportedBundleFilename))
		if err != nil {
			return core.Envelope{}, fmt.Errorf("%w: load semantic catalog: %v", transport.ErrExecutionConfiguration, err)
		}
		return executeCatalogOperationWithStore(ctx, catalog, store, operation)
	}
	catalog, closeCatalog, err := loadCatalog(ctx)
	if err != nil {
		return core.Envelope{}, fmt.Errorf("%w: %v", transport.ErrExecutionConfiguration, err)
	}
	defer closeCatalog()
	return executeCatalogOperation(ctx, catalog, operation)
}

func executeCatalogOperation(ctx context.Context, catalog *registry.Catalog, operation core.Operation) (core.Envelope, error) {
	return executeCatalogOperationWithStore(ctx, catalog, nil, operation)
}

func executeCatalogOperationWithStore(ctx context.Context, catalog *registry.Catalog, store repository.Store, operation core.Operation) (core.Envelope, error) {
	paths, err := resolveCLIPaths()
	if err != nil {
		return core.Envelope{}, fmt.Errorf("%w: %v", transport.ErrExecutionConfiguration, err)
	}
	feedAdapter := adapter.FeedAdapter{Cache: adapter.NewFileFeedCache(filepath.Join(paths.CacheDir, "feeds"))}
	browserClient := browser.NewClient(paths.RuntimeDir)
	service := query.Service{
		Feed: feedAdapter, RSSHub: adapter.RSSHubAdapter{Feed: feedAdapter}, GitHub: adapter.GitHubAdapter{},
		Tavily: adapter.TavilyAdapter{}, XURL: adapter.XURLAdapter{}, Discourse: adapter.DiscourseAdapter{},
		DiscourseBrowser: adapter.DiscourseBrowserAdapter{Browser: browserClient},
		Arxiv:            adapter.ArxivAdapter{}, HNAlgolia: adapter.HNAlgoliaAdapter{}, CookieReader: browserClient,
	}
	if operation.SimilarityGrouping == core.SimilaritySemantic {
		if store == nil {
			return core.Envelope{}, fmt.Errorf("%w: semantic grouping requires a writable store", transport.ErrExecutionConfiguration)
		}
		service.Semantic = semantic.Service{Store: store}
	}
	envelope, err := service.Execute(ctx, catalog, operation)
	if errors.Is(err, semantic.ErrUnavailable) {
		return core.Envelope{}, fmt.Errorf("%w: %v", transport.ErrExecutionConfiguration, err)
	}
	return envelope, err
}

func loopbackListenAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func runRSSHubEndpointProbe(endpointID string, stderr io.Writer) (any, int) {
	catalog, closeCatalog, err := loadCatalog(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "omnihub: load catalog: %v\n", err)
		return nil, exitConfig
	}
	defer closeCatalog()
	endpoint, ok := catalog.Endpoint(strings.TrimSpace(endpointID))
	if !ok || endpoint.Provider != "rsshub" {
		fmt.Fprintf(stderr, "omnihub: probe RSSHub endpoint: endpoint %q is not configured\n", endpointID)
		return nil, exitConfig
	}
	egressProfile, egressCredential, reason := router.ResolveEgress(catalog, core.Channel{EndpointProfileID: endpoint.ID})
	if reason != "" {
		fmt.Fprintf(stderr, "omnihub: probe RSSHub endpoint: egress is unavailable (%s)\n", reason)
		return nil, exitConfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	probe := (adapter.RSSHubAdapter{}).ProbeEndpoint(ctx, endpoint, egressProfile, egressCredential)
	code := 0
	if !probe.Passed {
		code = exitFailed
		if probe.Error != nil && probe.Error.Code == core.ErrorConfig {
			code = exitConfig
		}
	}
	return rssHubEndpointProbeOutput{SchemaVersion: core.SchemaVersion, EndpointProfileID: endpoint.ID, Probe: probe}, code
}

func runChannelProbe(channelID, idempotencyKey string, stderr io.Writer) (any, int) {
	paths, err := resolveCLIPaths()
	if err != nil {
		fmt.Fprintf(stderr, "omnihub: resolve paths: %v\n", err)
		return nil, exitConfig
	}
	store, err := sqlite.Open(context.Background(), paths.Database)
	if err != nil {
		fmt.Fprintf(stderr, "omnihub: open channel Probe store: %v\n", err)
		return nil, exitConfig
	}
	defer store.Close()
	catalog, err := registry.Load(context.Background(), store, filepath.Join(paths.ConfigDir, registry.ImportedBundleFilename))
	if err != nil {
		fmt.Fprintf(stderr, "omnihub: load channel Probe catalog: %v\n", err)
		return nil, exitConfig
	}
	instanceID, err := core.NewRequestID()
	if err != nil {
		fmt.Fprintf(stderr, "omnihub: create channel Probe identity: %v\n", err)
		return nil, exitInternal
	}
	if idempotencyKey == "" {
		idempotencyKey = instanceID
	}
	service := health.Service{Store: store, Catalog: catalog, InstanceID: instanceID}
	run, _, err := service.CreateProbeRun(context.Background(), strings.TrimSpace(channelID), idempotencyKey)
	if err != nil {
		fmt.Fprintf(stderr, "omnihub: create channel Probe run: %v\n", err)
		if errors.Is(err, repository.ErrNotFound) || errors.Is(err, repository.ErrIdempotency) || errors.Is(err, health.ErrInvalidProbeRun) {
			return nil, exitConfig
		}
		return nil, exitInternal
	}
	now := time.Now().UTC()
	if run.Status == core.RunQueued || run.Status == core.RunRunning && (run.LeaseExpiresAt == nil || !run.LeaseExpiresAt.After(now)) {
		run, err = service.ProcessRun(context.Background(), run.ID)
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: process channel Probe run: %v\n", err)
			return nil, exitInternal
		}
	}
	code := 0
	if run.Status == core.RunFailed {
		code = exitFailed
		if run.LastError != nil && run.LastError.Code == core.ErrorConfig {
			code = exitConfig
		}
	}
	output := channelProbeOutput{SchemaVersion: core.SchemaVersion, Run: run}
	records, listErr := store.ListProbeHealth(context.Background(), repository.ProbeHealthFilter{ChannelID: strings.TrimSpace(channelID), Limit: 1})
	if listErr != nil {
		fmt.Fprintf(stderr, "omnihub: read channel Probe report: %v\n", listErr)
		return nil, exitInternal
	}
	if len(records) > 0 {
		output.Probe = &records[0]
	}
	return output, code
}

type transientDirectFeed struct {
	source  core.Source
	channel core.Channel
	egress  core.EgressProfile
	format  string
}

func queryOperation(kind core.OperationKind, args []string, stdin io.Reader, stderr io.Writer) (core.Operation, *transientDirectFeed, error) {
	if len(args) == 0 {
		switch kind {
		case core.OperationLatest:
			input, err := decodeStrictJSON[core.LatestInput](stdin)
			return input.OperationRequest(), nil, err
		case core.OperationSearch:
			input, err := decodeStrictJSON[core.SearchInput](stdin)
			return input.OperationRequest(), nil, err
		case core.OperationFetch:
			input, err := decodeStrictJSON[core.FetchInput](stdin)
			return input.OperationRequest(), nil, err
		default:
			return core.Operation{}, nil, fmt.Errorf("unsupported operation %q", kind)
		}
	}

	if kind == core.OperationFetch {
		return core.Operation{}, nil, errors.New("fetch only accepts typed JSON input")
	}
	if kind == core.OperationSearch {
		operation, err := searchFlagOperation(args, stderr)
		return operation, nil, err
	}
	flags := flag.NewFlagSet(string(kind), flag.ContinueOnError)
	flags.SetOutput(stderr)
	options := directFeedFlags{}
	flags.StringVar(&options.feedURL, "feed-url", "", "absolute RSS, Atom, JSON Feed, or discovery page URL")
	flags.StringVar(&options.source, "source", "", "logical source ID")
	flags.StringVar(&options.egressMode, "egress-mode", "", "explicit transient egress (direct or environment)")
	flags.StringVar(&options.format, "format", "json", "output format (json or jsonl)")
	flags.StringVar(&options.identity, "identity-dedupe", string(core.IdentityExact), "identity dedupe mode (exact or none)")
	flags.StringVar(&options.from, "from", "", "inclusive RFC3339 lower time bound")
	flags.StringVar(&options.to, "to", "", "inclusive RFC3339 upper time bound")
	flags.IntVar(&options.limit, "limit", 20, "maximum returned items")
	flags.IntVar(&options.deadlineMS, "deadline-ms", 30000, "whole-operation deadline in milliseconds")
	if err := flags.Parse(args); err != nil {
		return core.Operation{}, nil, err
	}
	if flags.NArg() != 0 {
		return core.Operation{}, nil, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if strings.TrimSpace(options.feedURL) == "" || strings.TrimSpace(options.source) == "" {
		return core.Operation{}, nil, errors.New("--feed-url and --source are required for flag-based queries")
	}
	egressMode := core.EgressMode(strings.TrimSpace(options.egressMode))
	if egressMode != core.EgressModeDirect && egressMode != core.EgressModeEnvironment {
		return core.Operation{}, nil, errors.New("--egress-mode must be direct or environment for flag-based queries")
	}
	if options.format != "json" && options.format != "jsonl" {
		return core.Operation{}, nil, fmt.Errorf("unsupported format %q", options.format)
	}
	normalizedURL, err := adapter.NormalizeFeedURL(options.feedURL)
	if err != nil {
		return core.Operation{}, nil, err
	}
	timeRange, err := parseTimeRange(options.from, options.to)
	if err != nil {
		return core.Operation{}, nil, err
	}

	sourceID := strings.TrimSpace(options.source)
	digest := sha256.Sum256([]byte(sourceID + "\x00" + normalizedURL))
	channelID := fmt.Sprintf("channel_ephemeral_feed_%x", digest)
	egressID := "egress_ephemeral_" + string(egressMode)
	operation := core.Operation{
		SchemaVersion:      core.SchemaVersion,
		Operation:          kind,
		Scope:              core.Scope{Channels: []string{channelID}},
		RoutePolicy:        core.RoutePolicy{Mode: core.RouteAuto, Aggregate: false, AllowFallback: false},
		Limit:              options.limit,
		TimeRange:          timeRange,
		IdentityDedupe:     core.IdentityDedupe(options.identity),
		SimilarityGrouping: core.SimilarityOff,
		DeadlineMS:         options.deadlineMS,
	}
	if err := operation.Validate(); err != nil {
		return core.Operation{}, nil, err
	}
	return operation, &transientDirectFeed{
		source: core.Source{ID: sourceID, DisplayName: sourceID, Origin: "user", Enabled: true},
		channel: core.Channel{
			ID: channelID, DisplayName: sourceID, Source: sourceID,
			RouteTemplateID: "direct-feed-window", Parameters: map[string]any{"url": normalizedURL},
			EgressProfileID: egressID, Priority: 100, Enabled: true, Revision: 1,
		},
		egress: core.EgressProfile{ID: egressID, Mode: egressMode, Enabled: true, Revision: 1},
		format: options.format,
	}, nil
}

func searchFlagOperation(args []string, stderr io.Writer) (core.Operation, error) {
	flags := flag.NewFlagSet("search", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var source, query, from, to, sort, authors, categories, tags, contentFields, identity string
	var limit, deadlineMS int
	flags.StringVar(&source, "source", "", "logical source ID")
	flags.StringVar(&query, "query", "", "text query")
	flags.StringVar(&from, "from", "", "inclusive RFC3339 published_at lower bound")
	flags.StringVar(&to, "to", "", "inclusive RFC3339 published_at upper bound")
	flags.StringVar(&sort, "sort", string(core.SearchSortRelevance), "result sort (relevance or newest)")
	flags.StringVar(&authors, "authors", "", "comma-separated authors")
	flags.StringVar(&categories, "categories", "", "comma-separated categories")
	flags.StringVar(&tags, "tags", "", "comma-separated tags")
	flags.StringVar(&contentFields, "content-fields", "", "comma-separated title,body,first_post")
	flags.StringVar(&identity, "identity-dedupe", string(core.IdentityExact), "identity dedupe mode (exact or none)")
	flags.IntVar(&limit, "limit", 20, "maximum returned items")
	flags.IntVar(&deadlineMS, "deadline-ms", 30000, "whole-operation deadline in milliseconds")
	if err := flags.Parse(args); err != nil {
		return core.Operation{}, err
	}
	if flags.NArg() != 0 {
		return core.Operation{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if strings.TrimSpace(source) == "" || strings.TrimSpace(query) == "" {
		return core.Operation{}, errors.New("--source and --query are required for search")
	}
	timeRange, err := parseTimeRange(from, to)
	if err != nil {
		return core.Operation{}, err
	}
	queryText := strings.TrimSpace(query)
	operation := core.Operation{
		SchemaVersion: core.SchemaVersion, Operation: core.OperationSearch,
		Scope:       core.Scope{Sources: []string{strings.TrimSpace(source)}},
		RoutePolicy: core.RoutePolicy{Mode: core.RouteAuto, Aggregate: false, AllowFallback: true},
		Query:       &queryText, Limit: limit,
		Constraints: core.SearchConstraints{
			Time:    core.SearchTimeConstraint{Field: core.SearchTimePublishedAt, From: timeRange.From, To: timeRange.To},
			Authors: splitSearchFlag(authors), Categories: splitSearchFlag(categories), Tags: splitSearchFlag(tags),
		},
		Sort: core.SearchSort(strings.TrimSpace(sort)), IdentityDedupe: core.IdentityDedupe(identity), SimilarityGrouping: core.SimilarityOff, DeadlineMS: deadlineMS,
	}
	for _, field := range splitSearchFlag(contentFields) {
		operation.Constraints.ContentFields = append(operation.Constraints.ContentFields, core.SearchContentField(field))
	}
	if operation.Constraints.Time.From == nil && operation.Constraints.Time.To == nil {
		operation.Constraints.Time.Field = ""
	}
	if err := operation.Validate(); err != nil {
		return core.Operation{}, err
	}
	return operation, nil
}

func splitSearchFlag(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func parseTimeRange(from, to string) (core.TimeRange, error) {
	result := core.TimeRange{}
	for _, bound := range []struct {
		raw    string
		target **time.Time
	}{{raw: from, target: &result.From}, {raw: to, target: &result.To}} {
		raw, target := bound.raw, bound.target
		if strings.TrimSpace(raw) == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return core.TimeRange{}, fmt.Errorf("parse time bound %q as RFC3339: %w", raw, err)
		}
		utc := parsed.UTC()
		*target = &utc
	}
	if result.From != nil && result.To != nil && result.From.After(*result.To) {
		return core.TimeRange{}, errors.New("--from must not be after --to")
	}
	return result, nil
}

func decodeStrictJSON[T any](reader io.Reader) (T, error) {
	var value T
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("more than one JSON value")
		}
		return value, err
	}
	return value, nil
}

func revisionFlag(name string, args []string, stderr io.Writer) (int64, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	var expectedRevision int64
	flags.Int64Var(&expectedRevision, "revision", 0, "expected resource revision")
	if err := flags.Parse(args); err != nil {
		return 0, err
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "omnihub: %s: unexpected positional arguments\n", name)
		return 0, errors.New("unexpected positional arguments")
	}
	return expectedRevision, nil
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
	// SQLite v5，并以 mode=ro 打开；初始化与升级由后续写入命令负责。
	version, err := sqlite.SchemaVersion(ctx, databasePath)
	if err != nil {
		return nil, err
	}
	if version != sqlite.CurrentSchemaVersion {
		return nil, fmt.Errorf("SQLite schema version %d requires initialization or migration to version %d", version, sqlite.CurrentSchemaVersion)
	}
	return sqlite.OpenReadOnly(ctx, databasePath)
}

func openManagementService(ctx context.Context) (management.Service, func(), error) {
	paths, err := resolveCLIPaths()
	if err != nil {
		return management.Service{}, func() {}, fmt.Errorf("resolve paths: %w", err)
	}
	store, err := sqlite.Open(ctx, paths.Database)
	if err != nil {
		return management.Service{}, func() {}, err
	}
	catalog, err := registry.Load(ctx, store, filepath.Join(paths.ConfigDir, registry.ImportedBundleFilename))
	if err != nil {
		_ = store.Close()
		return management.Service{}, func() {}, err
	}
	return management.Service{Store: store, Catalog: catalog}, func() { _ = store.Close() }, nil
}

// openReadManagementService 为 OPML/Credential 读取复用同一无副作用入口。
// 缺数据库时使用内存 Store，不创建用户目录或 SQLite 文件。
func openReadManagementService(ctx context.Context) (management.Service, func(), error) {
	paths, err := resolveCLIPaths()
	if err != nil {
		return management.Service{}, func() {}, fmt.Errorf("resolve paths: %w", err)
	}
	bundlePath := filepath.Join(paths.ConfigDir, registry.ImportedBundleFilename)
	if _, err := os.Stat(paths.Database); errors.Is(err, os.ErrNotExist) {
		store, openErr := sqlite.Open(ctx, ":memory:")
		if openErr != nil {
			return management.Service{}, func() {}, openErr
		}
		catalog, loadErr := registry.Load(ctx, store, bundlePath)
		if loadErr != nil {
			_ = store.Close()
			return management.Service{}, func() {}, loadErr
		}
		return management.Service{Store: store, Catalog: catalog}, func() { _ = store.Close() }, nil
	} else if err != nil {
		return management.Service{}, func() {}, fmt.Errorf("inspect database: %w", err)
	}
	store, err := openCatalogStore(ctx, paths.Database)
	if err != nil {
		return management.Service{}, func() {}, err
	}
	catalog, err := registry.Load(ctx, store, bundlePath)
	if err != nil {
		_ = store.Close()
		return management.Service{}, func() {}, err
	}
	return management.Service{Store: store, Catalog: catalog}, func() { _ = store.Close() }, nil
}

func managementExitCode(err error) int {
	if errors.Is(err, management.ErrInvalidDirectFeed) || errors.Is(err, management.ErrInvalidEgress) || errors.Is(err, management.ErrInvalidOPML) || errors.Is(err, management.ErrInvalidRSSHub) || errors.Is(err, management.ErrInvalidProviderConfig) || errors.Is(err, management.ErrInvalidSemantic) {
		return exitParameter
	}
	return exitConfig
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
	if value, exists := os.LookupEnv("OMNIHUB_CACHE_DIR"); exists {
		if value == "" {
			return config.Paths{}, errors.New("OMNIHUB_CACHE_DIR must not be empty")
		}
		paths.CacheDir = value
	}
	if value, exists := os.LookupEnv("OMNIHUB_RUNTIME_DIR"); exists {
		if value == "" {
			return config.Paths{}, errors.New("OMNIHUB_RUNTIME_DIR must not be empty")
		}
		paths.RuntimeDir = value
	}
	return paths, nil
}

func buildVersion() versionOutput {
	result := versionOutput{
		Version: strings.TrimSpace(releaseVersion),
		Commit:  strings.TrimSpace(releaseCommit),
		Date:    strings.TrimSpace(releaseDate),
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if result.Version == "" {
			result.Version = strings.TrimSpace(info.Main.Version)
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if result.Commit == "" {
					result.Commit = strings.TrimSpace(setting.Value)
				}
			case "vcs.time":
				if result.Date == "" {
					result.Date = strings.TrimSpace(setting.Value)
				}
			case "vcs.modified":
				result.Modified = setting.Value == "true"
			}
		}
	}
	if result.Version == "" {
		result.Version = "(devel)"
	}
	if result.Commit == "" {
		result.Commit = "unknown"
	}
	if result.Date == "" {
		result.Date = "unknown"
	}
	return result
}

func runChromeHost(stdin io.Reader, stdout, stderr io.Writer) int {
	paths, err := resolveCLIPaths()
	if err != nil {
		fmt.Fprintf(stderr, "omnihub: resolve Chrome host paths: %v\n", err)
		return exitConfig
	}
	if err := browser.RunHost(context.Background(), stdin, stdout, paths.RuntimeDir, time.Now); err != nil {
		fmt.Fprintf(stderr, "omnihub: run Chrome native host: %v\n", err)
		return exitFailed
	}
	return 0
}
