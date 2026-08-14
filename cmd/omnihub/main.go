package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ylxmf2005/omnihub/internal/adapter"
	"github.com/ylxmf2005/omnihub/internal/config"
	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/management"
	"github.com/ylxmf2005/omnihub/internal/query"
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
  omnihub egress-profiles
  omnihub egress-profiles apply < egress-profile.json
  omnihub egress-profiles disable ID --revision N
  omnihub endpoints
  omnihub endpoints apply < endpoint.json
  omnihub endpoints disable ID --revision N
  omnihub endpoints probe ID
  omnihub credentials
  omnihub credentials apply < credential.json
  omnihub channels
  omnihub channels apply < direct-feed.json
  omnihub channels apply-rsshub < rsshub-channel.json
  omnihub channels disable ID --revision N
  omnihub channels probe ID
  omnihub opml import --egress-profile ID < subscriptions.opml
  omnihub opml export > subscriptions.opml
  omnihub doctor --json
  omnihub plan < operation.json
  omnihub latest < latest.json
  omnihub search < search.json
  omnihub latest --feed-url URL --source ID --egress-mode direct|environment [--limit N] [--format json]
  omnihub search --feed-url URL --source ID --egress-mode direct|environment --query QUERY [--limit N] [--format json]

plan only selects declared channels; it never executes an upstream request.
`

const (
	exitInternal  = 1
	exitParameter = 3
	exitConfig    = 4
	exitFailed    = 5
)

type catalogOutput struct {
	SchemaVersion  string                       `json:"schema_version"`
	Sources        *[]core.Source               `json:"sources,omitempty"`
	Providers      *[]core.Provider             `json:"providers,omitempty"`
	RouteTemplates *[]core.RouteTemplate        `json:"route_templates,omitempty"`
	EgressProfiles *[]core.EgressProfileSummary `json:"egress_profiles,omitempty"`
	Endpoints      *[]core.EndpointProfile      `json:"endpoints,omitempty"`
	Credentials    *[]core.CredentialSummary    `json:"credentials,omitempty"`
	Channels       *[]core.Channel              `json:"channels,omitempty"`
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

type rssHubChannelProbeOutput struct {
	SchemaVersion     string                    `json:"schema_version"`
	ChannelID         string                    `json:"channel_id"`
	EndpointProfileID string                    `json:"endpoint_profile_id"`
	Probe             adapter.RSSHubProbeReport `json:"probe"`
}

type directFeedChannelProbeOutput struct {
	SchemaVersion string                  `json:"schema_version"`
	ChannelID     string                  `json:"channel_id"`
	Probe         adapter.FeedProbeReport `json:"probe"`
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
		if len(args) < 2 || args[1] != "apply" && args[1] != "disable" && args[1] != "probe" {
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
		if len(args) < 2 || args[1] != "apply" && args[1] != "apply-rsshub" && args[1] != "disable" && args[1] != "probe" {
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
			if len(args) != 3 {
				fmt.Fprint(stderr, usage)
				return exitParameter
			}
			probe, code := runChannelProbe(args[2], stderr)
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

	case "latest", "search":
		operationKind := core.OperationKind(args[0])
		result, code := runQueryCommand(operationKind, args[1:], stdin, stderr)
		if result == nil {
			return code
		}
		value = result
		resultExitCode = code

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

func runQueryCommand(kind core.OperationKind, args []string, stdin io.Reader, stderr io.Writer) (any, int) {
	operation, transient, err := queryOperation(kind, args, stdin, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "omnihub: decode %s operation: %v\n", kind, err)
		return nil, exitParameter
	}

	catalog, closeCatalog, err := loadCatalog(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "omnihub: load catalog: %v\n", err)
		return nil, exitConfig
	}
	defer closeCatalog()
	if transient != nil {
		catalog, err = catalog.WithEgressProfile(transient.egress)
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: configure direct feed egress: %v\n", err)
			return nil, exitConfig
		}
		catalog, err = catalog.WithSourceAndChannel(transient.source, transient.channel)
		if err != nil {
			fmt.Fprintf(stderr, "omnihub: configure direct feed: %v\n", err)
			return nil, exitConfig
		}
	}

	paths, err := resolveCLIPaths()
	if err != nil {
		fmt.Fprintf(stderr, "omnihub: resolve paths: %v\n", err)
		return nil, exitConfig
	}
	feedAdapter := adapter.FeedAdapter{Cache: adapter.NewFileFeedCache(filepath.Join(paths.CacheDir, "feeds"))}
	service := query.Service{Feed: feedAdapter, RSSHub: adapter.RSSHubAdapter{Feed: feedAdapter}}
	envelope, err := service.Execute(context.Background(), catalog, operation)
	if err != nil {
		if errors.Is(err, router.ErrNoRoute) {
			fmt.Fprintf(stderr, "omnihub: execute %s: %v\n", kind, err)
			return nil, exitConfig
		}
		fmt.Fprintf(stderr, "omnihub: execute %s: %v\n", kind, err)
		if errors.Is(err, core.ErrInvalidOperation) {
			return nil, exitParameter
		}
		return nil, exitInternal
	}
	if envelope.Status == core.StatusFailed {
		return envelope, exitFailed
	}
	return envelope, 0
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

func runChannelProbe(channelID string, stderr io.Writer) (any, int) {
	catalog, closeCatalog, err := loadCatalog(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "omnihub: load catalog: %v\n", err)
		return nil, exitConfig
	}
	defer closeCatalog()
	channel, ok := catalog.Channel(strings.TrimSpace(channelID))
	if !ok {
		fmt.Fprintf(stderr, "omnihub: probe channel: channel %q is not configured\n", channelID)
		return nil, exitConfig
	}
	template, ok := catalog.RouteTemplate(channel.RouteTemplateID)
	if !ok || template.Adapter != "feed" && template.Adapter != "rsshub" {
		fmt.Fprintf(stderr, "omnihub: probe channel: channel %q has no supported probe adapter\n", channelID)
		return nil, exitConfig
	}
	egressProfile, egressCredential, reason := router.ResolveEgress(catalog, channel)
	if reason != "" {
		fmt.Fprintf(stderr, "omnihub: probe channel: egress is unavailable (%s)\n", reason)
		return nil, exitConfig
	}
	operation := core.Operation{
		SchemaVersion: core.SchemaVersion, Operation: core.OperationLatest,
		Scope: core.Scope{Channels: []string{channel.ID}}, RoutePolicy: core.RoutePolicy{Mode: core.RouteAuto},
		Limit: 100, IdentityDedupe: core.IdentityExact, SimilarityGrouping: core.SimilarityOff, DeadlineMS: 30000,
	}
	ctx, cancel, err := operation.Context(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "omnihub: probe channel: %v\n", err)
		return nil, exitInternal
	}
	defer cancel()

	if template.Adapter == "feed" {
		probe := (adapter.FeedAdapter{}).Probe(ctx, adapter.FeedRequest{
			Operation: operation, Channel: channel, RouteTemplate: template,
			Egress: egressProfile, EgressCredential: egressCredential,
		})
		code := 0
		if len(probe.Result.Errors) > 0 {
			code = exitFailed
			if probe.Result.Errors[0].Code == core.ErrorConfig {
				code = exitConfig
			}
		}
		return directFeedChannelProbeOutput{SchemaVersion: core.SchemaVersion, ChannelID: channel.ID, Probe: probe}, code
	}

	endpoint, ok := catalog.Endpoint(channel.EndpointProfileID)
	if !ok {
		fmt.Fprintf(stderr, "omnihub: probe RSSHub channel: endpoint %q is not configured\n", channel.EndpointProfileID)
		return nil, exitConfig
	}
	var credential *core.Credential
	if channel.CredentialID != "" {
		resolved, exists := catalog.Credential(channel.CredentialID)
		if !exists || !resolved.Enabled || resolved.Value == nil {
			fmt.Fprintf(stderr, "omnihub: probe RSSHub channel: credential %q is unresolved\n", channel.CredentialID)
			return nil, exitConfig
		}
		credential = &resolved
	}
	probe := (adapter.RSSHubAdapter{}).Probe(ctx, adapter.RSSHubRequest{
		Operation: operation, Channel: channel, RouteTemplate: template, Endpoint: endpoint, Credential: credential,
		Egress: egressProfile, EgressCredential: egressCredential,
	})
	code := 0
	if probe.Readiness == "failed" {
		code = exitFailed
	}
	return rssHubChannelProbeOutput{
		SchemaVersion: core.SchemaVersion, ChannelID: channel.ID, EndpointProfileID: endpoint.ID, Probe: probe,
	}, code
}

type transientDirectFeed struct {
	source  core.Source
	channel core.Channel
	egress  core.EgressProfile
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
		default:
			return core.Operation{}, nil, fmt.Errorf("unsupported operation %q", kind)
		}
	}

	flags := flag.NewFlagSet(string(kind), flag.ContinueOnError)
	flags.SetOutput(stderr)
	options := directFeedFlags{}
	flags.StringVar(&options.feedURL, "feed-url", "", "absolute RSS, Atom, JSON Feed, or discovery page URL")
	flags.StringVar(&options.source, "source", "", "logical source ID")
	flags.StringVar(&options.egressMode, "egress-mode", "", "explicit transient egress (direct or environment)")
	flags.StringVar(&options.query, "query", "", "bounded-window search query")
	flags.StringVar(&options.format, "format", "json", "output format (json)")
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
	if options.format != "json" {
		return core.Operation{}, nil, fmt.Errorf("unsupported format %q", options.format)
	}
	if kind == core.OperationSearch && strings.TrimSpace(options.query) == "" {
		return core.Operation{}, nil, errors.New("--query is required for search")
	}
	if kind == core.OperationLatest && options.query != "" {
		return core.Operation{}, nil, errors.New("--query is only valid for search")
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
	if kind == core.OperationSearch {
		query := strings.TrimSpace(options.query)
		operation.Query = &query
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
	}, nil
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
	if errors.Is(err, management.ErrInvalidDirectFeed) || errors.Is(err, management.ErrInvalidEgress) || errors.Is(err, management.ErrInvalidOPML) || errors.Is(err, management.ErrInvalidRSSHub) {
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
	return paths, nil
}
