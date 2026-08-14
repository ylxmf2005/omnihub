package health

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ylxmf2005/omnihub/internal/adapter"
	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/registry"
	"github.com/ylxmf2005/omnihub/internal/repository"
	"github.com/ylxmf2005/omnihub/internal/router"
)

const (
	probeRunKind      = "channel_probe"
	probeTimeout      = 30 * time.Second
	probeLease        = 35 * time.Second
	passedProbeTTL    = 15 * time.Minute
	failedProbeTTL    = 15 * time.Minute
	transientProbeTTL = 5 * time.Minute
)

var (
	ErrInvalidService   = errors.New("invalid health service")
	ErrInvalidProbeRun  = errors.New("invalid channel probe run")
	ErrUnsupportedProbe = errors.New("unsupported channel probe")
)

// Store 是显式 Probe Run 所需的最窄 Repository 边界。
type Store interface {
	CreateRun(context.Context, repository.CreateRun) (core.Run, bool, error)
	GetRun(context.Context, string) (core.Run, error)
	ClaimRun(context.Context, repository.ClaimRun) (core.Run, error)
	FinishRun(context.Context, repository.FinishRun) (core.Run, error)
	CompleteProbeRun(context.Context, core.ChannelProbeRecord, repository.FinishRun) (core.Run, error)
}

type FeedProber interface {
	Probe(context.Context, adapter.FeedRequest) adapter.FeedProbeReport
}

type RSSHubProber interface {
	Probe(context.Context, adapter.RSSHubRequest) adapter.RSSHubProbeReport
}

// Service 不包含 scheduler 或后台循环；调用方显式创建 Run，再显式处理它。
type Service struct {
	Store      Store
	Catalog    *registry.Catalog
	Feed       FeedProber
	RSSHub     RSSHubProber
	InstanceID string
	Now        func() time.Time
}

// CreateProbeRun 只持久化 queued Run，不探测网络。
func (service Service) CreateProbeRun(ctx context.Context, channelID, idempotencyKey string) (core.Run, bool, error) {
	if err := service.validate(); err != nil {
		return core.Run{}, false, err
	}
	if channelID == "" || channelID != strings.TrimSpace(channelID) || idempotencyKey == "" || idempotencyKey != strings.TrimSpace(idempotencyKey) {
		return core.Run{}, false, fmt.Errorf("%w: channel and idempotency key are required", ErrInvalidProbeRun)
	}
	if _, ok := service.Catalog.Channel(channelID); !ok {
		return core.Run{}, false, fmt.Errorf("%w: channel %s: %w", ErrInvalidProbeRun, channelID, repository.ErrNotFound)
	}
	runID, err := randomID("run_")
	if err != nil {
		return core.Run{}, false, fmt.Errorf("create probe run id: %w", err)
	}
	requestID, err := core.NewRequestID()
	if err != nil {
		return core.Run{}, false, fmt.Errorf("create probe request id: %w", err)
	}
	resource := core.ResourceRef{Type: "channel", ID: channelID}
	payload, _ := json.Marshal(struct {
		Kind     string           `json:"kind"`
		Resource core.ResourceRef `json:"resource"`
	}{Kind: probeRunKind, Resource: resource})
	digest := sha256.Sum256(payload)
	run, created, err := service.Store.CreateRun(ctx, repository.CreateRun{
		ID: runID, Kind: probeRunKind, Resource: resource, RequestID: requestID,
		IdempotencyKey: idempotencyKey, PayloadHash: fmt.Sprintf("%x", digest),
		Progress: core.RunProgress{ChannelsTotal: 1}, CreatedAt: service.now(),
	})
	if err != nil {
		return core.Run{}, false, fmt.Errorf("create channel probe run: %w", err)
	}
	return run, created, nil
}

// ProcessRun claim 成功后才执行一次真实 Probe。GitHub、Tavily 与 xurl 没有
// 分层 Probe 合同，因而只产生显式 unsupported 终态，不运行普通 Query。
func (service Service) ProcessRun(ctx context.Context, runID string) (core.Run, error) {
	if err := service.validate(); err != nil {
		return core.Run{}, err
	}
	if runID == "" || runID != strings.TrimSpace(runID) {
		return core.Run{}, fmt.Errorf("%w: run id is required", ErrInvalidProbeRun)
	}
	run, err := service.Store.GetRun(ctx, runID)
	if err != nil {
		return core.Run{}, fmt.Errorf("load channel probe run: %w", err)
	}
	if run.Kind != probeRunKind || run.Resource.Type != "channel" || run.Resource.ID == "" {
		return core.Run{}, fmt.Errorf("%w: run %s is not a channel probe", ErrInvalidProbeRun, runID)
	}
	if run.Status != core.RunQueued && run.Status != core.RunRunning {
		return run, nil
	}
	claimedAt := service.now()
	claimed, err := service.Store.ClaimRun(ctx, repository.ClaimRun{
		ID: run.ID, ExpectedRevision: run.Revision, InstanceID: service.InstanceID,
		Now: claimedAt, LeaseUntil: claimedAt.Add(probeLease),
	})
	if err != nil {
		return core.Run{}, fmt.Errorf("claim channel probe run: %w", err)
	}

	channel, ok := service.Catalog.Channel(claimed.Resource.ID)
	if !ok {
		return service.finishFailure(ctx, claimed, core.Channel{ID: claimed.Resource.ID}, core.RouteTemplate{}, "channel_missing", "Channel no longer exists")
	}
	template, ok := service.Catalog.RouteTemplate(channel.RouteTemplateID)
	if !ok {
		return service.finishFailure(ctx, claimed, channel, core.RouteTemplate{}, "template_missing", "RouteTemplate no longer exists")
	}
	if template.Adapter != "feed" && template.Adapter != "rsshub" {
		return service.finishFailure(ctx, claimed, channel, template, "probe_unsupported", ErrUnsupportedProbe.Error())
	}
	binding, problem := service.resolveBinding(channel, template)
	if problem != nil {
		return service.finishFailure(ctx, claimed, channel, template, "probe_config_invalid", problem.Message)
	}
	routeGroup, err := RouteGroupKey(service.Catalog, channel)
	if err != nil {
		return service.finishFailure(ctx, claimed, channel, template, "probe_config_invalid", "Channel Probe target is invalid")
	}

	probeContext, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	execution := service.executeProbe(probeContext, channel, template, binding)
	if execution.problem != nil {
		decorateProbeError(execution.problem, channel, template)
	}
	if execution.problem == nil && !execution.passed {
		execution.problem = &core.Error{Code: core.ErrorProtocol, Message: "Channel Probe returned no terminal result", Retryable: false}
		decorateProbeError(execution.problem, channel, template)
	}
	if execution.checkedAt.IsZero() {
		execution.checkedAt = service.now()
	}
	if execution.egress.ProfileID != binding.egress.ID || execution.egress.Mode != binding.egress.Mode {
		return service.finishFailure(ctx, claimed, channel, template, "probe_egress_mismatch", "Channel Probe returned inconsistent egress facts")
	}

	transient := transientProbeFailure(execution.problem)
	ttl := failedProbeTTL
	if execution.passed {
		ttl = passedProbeTTL
	} else if transient {
		ttl = transientProbeTTL
	}
	recordID, err := randomID("probe_")
	if err != nil {
		return core.Run{}, fmt.Errorf("create channel probe record id: %w", err)
	}
	record := core.ChannelProbeRecord{
		ID: recordID, ChannelID: channel.ID, RouteGroup: routeGroup, Egress: execution.egress,
		ChannelRevision: channel.Revision, EndpointRevision: binding.endpointRevision,
		EgressRevision: binding.egress.Revision, Passed: execution.passed, Transient: transient,
		CheckedAt: execution.checkedAt.UTC(), ExpiresAt: execution.checkedAt.UTC().Add(ttl), Report: execution.report,
	}
	if err := record.Validate(); err != nil {
		return service.finishFailure(ctx, claimed, channel, template, "probe_report_invalid", "Channel Probe report cannot be persisted safely")
	}
	finish := repository.FinishRun{
		ID: claimed.ID, ExpectedRevision: claimed.Revision, InstanceID: service.InstanceID,
		Now: service.now(), Status: core.RunComplete, Progress: core.RunProgress{ChannelsTotal: 1, ChannelsFinished: 1},
	}
	if !execution.passed {
		finish.Status, finish.LastError = core.RunFailed, execution.problem
	}
	completed, err := service.Store.CompleteProbeRun(ctx, record, finish)
	if err != nil {
		return core.Run{}, fmt.Errorf("complete channel probe run: %w", err)
	}
	return completed, nil
}

type probeBinding struct {
	endpoint         core.EndpointProfile
	endpointRevision int64
	credential       *core.Credential
	egress           core.EgressProfile
	egressCredential *core.Credential
}

func (service Service) resolveBinding(channel core.Channel, template core.RouteTemplate) (probeBinding, *core.Error) {
	failure := func(message string) (probeBinding, *core.Error) {
		return probeBinding{}, &core.Error{Code: core.ErrorConfig, Message: message, Retryable: false}
	}
	if !channel.Enabled {
		return failure("Channel is disabled")
	}
	if source, ok := service.Catalog.Source(channel.Source); !ok || !source.Enabled {
		return failure("Source is unavailable")
	}
	if !service.Catalog.TemplateEnabled(template.RouteTemplateID) {
		return failure("RouteTemplate is disabled")
	}
	if provider, ok := service.Catalog.Provider(template.Provider); !ok || !provider.Enabled {
		return failure("Provider is unavailable")
	}
	egress, egressCredential, reason := router.ResolveEgress(service.Catalog, channel)
	if reason != "" {
		return failure("Channel egress is unavailable")
	}
	binding := probeBinding{egress: egress, egressCredential: egressCredential}
	if channel.EndpointProfileID != "" {
		endpoint, ok := service.Catalog.Endpoint(channel.EndpointProfileID)
		if !ok || !endpoint.Enabled {
			return failure("Channel endpoint is unavailable")
		}
		binding.endpoint, binding.endpointRevision = endpoint, endpoint.Revision
	}
	if channel.CredentialID != "" {
		credential, ok := service.Catalog.Credential(channel.CredentialID)
		if !ok || !credential.Enabled || credential.Value == nil || strings.TrimSpace(*credential.Value) == "" {
			return failure("Channel credential is unavailable")
		}
		binding.credential = &credential
	} else if template.Auth.Required {
		return failure("Channel credential is required")
	}
	if template.Adapter == "feed" && (channel.EndpointProfileID != "" || channel.CredentialID != "") {
		return failure("Direct Feed Probe cannot use Endpoint or Credential")
	}
	if template.Adapter == "rsshub" {
		request := adapter.RSSHubRequest{Channel: channel, RouteTemplate: template, Endpoint: binding.endpoint, Credential: binding.credential, Egress: egress, EgressCredential: egressCredential}
		if err := adapter.ValidateRSSHubRequest(request); err != nil {
			return failure("RSSHub Probe binding is invalid")
		}
	}
	return binding, nil
}

type probeExecution struct {
	report    json.RawMessage
	egress    core.ExecutionEgress
	checkedAt time.Time
	passed    bool
	problem   *core.Error
}

func (service Service) executeProbe(ctx context.Context, channel core.Channel, template core.RouteTemplate, binding probeBinding) probeExecution {
	switch template.Adapter {
	case "feed":
		prober := service.Feed
		if prober == nil {
			prober = adapter.FeedAdapter{}
		}
		report := prober.Probe(ctx, adapter.FeedRequest{
			Channel: channel, RouteTemplate: template, Egress: binding.egress, EgressCredential: binding.egressCredential,
		})
		encoded, err := projectFeedReport(report)
		if err != nil {
			return probeExecution{problem: &core.Error{Code: core.ErrorInternal, Message: "encode Feed Probe report"}}
		}
		passed := len(report.Result.Errors) == 0 && len(report.Result.Coverage) > 0
		var problem *core.Error
		if !passed {
			problem = firstProbeError(report.Result.Errors)
		}
		return probeExecution{report: encoded, egress: report.Egress, checkedAt: report.CheckedAt, passed: passed, problem: problem}
	case "rsshub":
		prober := service.RSSHub
		if prober == nil {
			prober = adapter.RSSHubAdapter{}
		}
		report := prober.Probe(ctx, adapter.RSSHubRequest{
			Channel: channel, RouteTemplate: template, Endpoint: binding.endpoint, Credential: binding.credential,
			Egress: binding.egress, EgressCredential: binding.egressCredential,
		})
		encoded, err := projectRSSHubReport(report)
		if err != nil {
			return probeExecution{problem: &core.Error{Code: core.ErrorInternal, Message: "encode RSSHub Probe report"}}
		}
		passed := report.Readiness == "ready"
		var problem *core.Error
		if !passed {
			problem = firstNonNilProbeError(report.Feed.Error, report.Metadata.Error, report.Endpoint.Error)
		}
		return probeExecution{report: encoded, egress: report.Egress, checkedAt: report.CheckedAt, passed: passed, problem: problem}
	default:
		return probeExecution{problem: &core.Error{Code: core.ErrorConfig, Message: ErrUnsupportedProbe.Error()}}
	}
}

func firstProbeError(problems []core.Error) *core.Error {
	if len(problems) == 0 {
		return nil
	}
	problem := problems[0]
	return &problem
}

func firstNonNilProbeError(problems ...*core.Error) *core.Error {
	for _, problem := range problems {
		if problem != nil {
			copy := *problem
			return &copy
		}
	}
	return nil
}

func transientProbeFailure(problem *core.Error) bool {
	if problem == nil || !problem.Retryable {
		return false
	}
	switch problem.Code {
	case core.ErrorNetwork, core.ErrorTimeout, core.ErrorRateLimit, core.ErrorUpstream:
		return true
	default:
		return false
	}
}

func decorateProbeError(problem *core.Error, channel core.Channel, template core.RouteTemplate) {
	if problem == nil {
		return
	}
	problem.Source, problem.Provider = channel.Source, template.Provider
	problem.ChannelID, problem.RouteTemplateID = channel.ID, template.RouteTemplateID
}

func (service Service) finishFailure(ctx context.Context, run core.Run, channel core.Channel, template core.RouteTemplate, reason, message string) (core.Run, error) {
	problem := &core.Error{
		Code: core.ErrorConfig, Message: message, Source: channel.Source, Provider: template.Provider,
		ChannelID: channel.ID, RouteTemplateID: template.RouteTemplateID, Retryable: false,
		Details: map[string]any{"reason": reason},
	}
	finished, err := service.Store.FinishRun(ctx, repository.FinishRun{
		ID: run.ID, ExpectedRevision: run.Revision, InstanceID: service.InstanceID, Now: service.now(),
		Status: core.RunFailed, Progress: core.RunProgress{ChannelsTotal: 1, ChannelsFinished: 1}, LastError: problem,
	})
	if err != nil {
		return core.Run{}, fmt.Errorf("finish failed channel probe run: %w", err)
	}
	return finished, nil
}

func (service Service) validate() error {
	if service.Store == nil || service.Catalog == nil || service.InstanceID == "" || service.InstanceID != strings.TrimSpace(service.InstanceID) {
		return ErrInvalidService
	}
	return nil
}

func (service Service) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return time.Now().UTC()
}

func randomID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value[:]), nil
}
