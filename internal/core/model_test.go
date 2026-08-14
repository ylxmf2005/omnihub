package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOperationValidation(t *testing.T) {
	query := "agent search"
	target := "https://example.com/post"
	repositoryTarget := "owner/repo"
	base := Operation{
		SchemaVersion:      SchemaVersion,
		Operation:          OperationSearch,
		Query:              &query,
		Scope:              Scope{Sources: []string{"github"}},
		RoutePolicy:        RoutePolicy{Mode: RouteAuto, AllowFallback: true},
		Limit:              20,
		IdentityDedupe:     IdentityExact,
		SimilarityGrouping: SimilarityOff,
		DeadlineMS:         30000,
	}

	cases := []struct {
		name      string
		operation Operation
		wantError bool
	}{
		{name: "search", operation: base},
		{name: "search without query", operation: replace(base, func(value *Operation) { value.Query = nil }), wantError: true},
		{name: "latest", operation: replace(base, func(value *Operation) { value.Operation = OperationLatest; value.Query = nil })},
		{name: "latest with query", operation: replace(base, func(value *Operation) { value.Operation = OperationLatest }), wantError: true},
		{name: "fetch", operation: replace(base, func(value *Operation) { value.Operation = OperationFetch; value.Query = nil; value.Target = &target })},
		{name: "fetch secret query", operation: replace(base, func(value *Operation) {
			secretTarget := "https://example.com/post?access_token=must-not-enter-envelope"
			value.Operation = OperationFetch
			value.Query = nil
			value.Target = &secretTarget
		}), wantError: true},
		{name: "fetch upstream identifier", operation: replace(base, func(value *Operation) {
			value.Operation = OperationFetch
			value.Query = nil
			value.Target = &repositoryTarget
		})},
		{name: "fetch without target", operation: replace(base, func(value *Operation) { value.Operation = OperationFetch; value.Query = nil }), wantError: true},
		{name: "missing scope", operation: replace(base, func(value *Operation) { value.Scope = Scope{} }), wantError: true},
		{name: "domain search scope", operation: replace(base, func(value *Operation) { value.Scope = Scope{Domains: []string{"example.com"}} })},
		{name: "domain latest scope", operation: replace(base, func(value *Operation) {
			value.Operation = OperationLatest
			value.Query = nil
			value.Scope = Scope{Domains: []string{"example.com"}}
		}), wantError: true},
		{name: "invalid domain scope", operation: replace(base, func(value *Operation) { value.Scope = Scope{Domains: []string{"https://example.com"}} }), wantError: true},
		{name: "duplicate domain scope", operation: replace(base, func(value *Operation) { value.Scope = Scope{Domains: []string{"example.com", "EXAMPLE.COM"}} }), wantError: true},
		{name: "continuation unsupported", operation: replace(base, func(value *Operation) { token := "adapter-cursor"; value.Continuation = &token }), wantError: true},
		{name: "untyped selector", operation: replace(base, func(value *Operation) { value.RoutePolicy.Prefer = []RouteSelector{{ID: "github"}} }), wantError: true},
		{name: "auto with prefer hint", operation: replace(base, func(value *Operation) {
			value.RoutePolicy.Prefer = []RouteSelector{{Kind: SelectorChannel, ID: "github"}}
		})},
		{name: "prefer requires selectors", operation: replace(base, func(value *Operation) { value.RoutePolicy.Mode = RoutePrefer }), wantError: true},
		{name: "only requires selectors", operation: replace(base, func(value *Operation) { value.RoutePolicy.Mode = RouteOnly }), wantError: true},
		{name: "exclude requires selectors", operation: replace(base, func(value *Operation) { value.RoutePolicy.Mode = RouteExclude }), wantError: true},
		{name: "only and exclude compose", operation: replace(base, func(value *Operation) {
			value.RoutePolicy.Mode = RouteOnly
			value.RoutePolicy.Only = []RouteSelector{{Kind: SelectorProvider, ID: "github-api"}}
			value.RoutePolicy.Exclude = []RouteSelector{{Kind: SelectorChannel, ID: "github-legacy"}}
		})},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := test.operation.Validate()
			if test.wantError && !errors.Is(err, ErrInvalidOperation) {
				t.Fatalf("Validate() error = %v, want ErrInvalidOperation", err)
			}
			if !test.wantError && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestEnvelopeValidateRejectsBypassedBuilder(t *testing.T) {
	started := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	// 过期的上游 Expires 仍是有效事实，表示结果应立即进入 stale，而不是协议错误。
	freshUntil := started.Add(-24 * time.Hour)
	envelope, err := BuildEnvelope(EnvelopeInput{
		RequestID: "req_123e4567-e89b-42d3-a456-426614174000", Request: validSearchOperation(),
		RequiredChannelIDs: []string{"channel_primary"},
		Executions: []Execution{{
			ChannelID: "channel_primary", Selection: SelectionPrimary, Status: ExecutionCompleted, StartedAt: started,
			Egress: &ExecutionEgress{ProfileID: "egress_direct", Mode: EgressModeDirect}, FreshUntil: &freshUntil,
		}},
		StartedAt: started, FinishedAt: started.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("valid Envelope.Validate() error = %v", err)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip Envelope
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if err := roundTrip.Validate(); err != nil || roundTrip.Executions[0].FreshUntil == nil || !roundTrip.Executions[0].FreshUntil.Equal(freshUntil) {
		t.Fatalf("fresh_until round trip = %v, %v", roundTrip.Executions[0].FreshUntil, err)
	}

	cases := []struct {
		name   string
		mutate func(*Envelope)
	}{
		{name: "invalid request id", mutate: func(value *Envelope) { value.RequestID = "req_01" }},
		{name: "inconsistent status", mutate: func(value *Envelope) { value.Status = StatusPartial }},
		{name: "inconsistent result count", mutate: func(value *Envelope) { value.Meta.ResultCount = 1 }},
		{name: "unknown error code", mutate: func(value *Envelope) { value.Errors = []Error{{Code: "mystery"}} }},
		{name: "null items", mutate: func(value *Envelope) { value.Items = nil }},
		{name: "missing selected execution", mutate: func(value *Envelope) { value.SelectedChannelIDs = append(value.SelectedChannelIDs, "channel_missing") }},
		{name: "missing execution egress", mutate: func(value *Envelope) { value.Executions[0].Egress = nil }},
		{name: "direct reported proxied", mutate: func(value *Envelope) {
			value.Executions[0].Egress = &ExecutionEgress{ProfileID: "egress_direct", Mode: EgressModeDirect, Proxied: true}
		}},
		{name: "unsupported egress mode", mutate: func(value *Envelope) {
			value.Executions[0].Egress = &ExecutionEgress{ProfileID: "egress_direct", Mode: "automatic"}
		}},
		{name: "zero completed freshness", mutate: func(value *Envelope) {
			zero := time.Time{}
			value.Executions[0].FreshUntil = &zero
		}},
		{name: "non UTC completed freshness", mutate: func(value *Envelope) {
			nonUTC := freshUntil.In(time.FixedZone("UTC+8", 8*60*60))
			value.Executions[0].FreshUntil = &nonUTC
		}},
		{name: "failed freshness", mutate: func(value *Envelope) { value.Executions[0].Status = ExecutionFailed }},
		{name: "skipped freshness", mutate: func(value *Envelope) { value.Executions[0].Status = ExecutionSkipped }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			value := envelope
			value.Executions = append([]Execution(nil), envelope.Executions...)
			test.mutate(&value)
			if err := value.Validate(); !errors.Is(err, ErrInvalidEnvelope) {
				t.Fatalf("Envelope.Validate() error = %v, want ErrInvalidEnvelope", err)
			}
		})
	}
}

func TestRequestLifecycle(t *testing.T) {
	operation := validSearchOperation()
	requestID, err := NewRequestID()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(requestID, "req_") || !validRequestID(requestID) {
		t.Fatalf("NewRequestID() = %q, want req_ UUIDv4", requestID)
	}
	second, err := NewRequestID()
	if err != nil {
		t.Fatal(err)
	}
	if second == requestID {
		t.Fatalf("consecutive request IDs are equal: %q", requestID)
	}

	// 比较 deadline 而不是等待超时，测试可以确定性证明父 deadline 自然优先。
	now := time.Now()
	parentDeadline := now.Add(2 * time.Second)
	parent, parentCancel := context.WithDeadline(context.Background(), parentDeadline)
	defer parentCancel()
	ctx, cancel, err := operation.Context(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	gotDeadline, ok := ctx.Deadline()
	if !ok || !gotDeadline.Equal(parentDeadline) {
		t.Fatalf("Context() deadline = %v, %v, want parent %v", gotDeadline, ok, parentDeadline)
	}

	operation.DeadlineMS = 250
	ctx, cancel, err = operation.Context(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	gotDeadline, ok = ctx.Deadline()
	if !ok {
		t.Fatal("Context() has no operation deadline")
	}
	remaining := time.Until(gotDeadline)
	if remaining <= 0 || remaining > 250*time.Millisecond {
		t.Fatalf("Context() remaining deadline = %v, want (0, 250ms]", remaining)
	}

	invalid := operation
	invalid.Query = nil
	if _, _, err := invalid.Context(context.Background()); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("invalid Context() error = %v, want ErrInvalidOperation", err)
	}
}

func TestBuildEnvelopeAggregatesTerminalStatus(t *testing.T) {
	started := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	base := EnvelopeInput{
		RequestID:          "req_123e4567-e89b-42d3-a456-426614174000",
		Request:            validSearchOperation(),
		RequiredChannelIDs: []string{"channel_primary"},
		StartedAt:          started,
		FinishedAt:         started.Add(1500 * time.Millisecond),
	}
	completed := Execution{
		ChannelID: "channel_primary", Status: ExecutionCompleted, Selection: "primary", StartedAt: started, DurationMS: 100,
		Egress: &ExecutionEgress{ProfileID: "egress_direct", Mode: EgressModeDirect},
	}
	failed := Execution{
		ChannelID: "channel_failed", Status: ExecutionFailed, Selection: "aggregate", StartedAt: started, DurationMS: 80,
		Egress: &ExecutionEgress{ProfileID: "egress_proxy", Mode: EgressModeHTTPProxy, Proxied: true},
	}
	skipped := Execution{ChannelID: "channel_skipped", Status: ExecutionSkipped, Selection: "candidate"}
	exhaustive := true
	gap := false

	cases := []struct {
		name   string
		mutate func(*EnvelopeInput)
		want   Status
	}{
		{name: "complete including skipped", mutate: func(input *EnvelopeInput) {
			input.Executions = []Execution{completed, skipped}
			input.Coverage = []Coverage{{Exhaustive: &exhaustive}}
		}, want: StatusComplete},
		{name: "complete proxy cache hit reports not proxied", mutate: func(input *EnvelopeInput) {
			cached := completed
			cached.Egress = &ExecutionEgress{ProfileID: "egress_proxy", Mode: EgressModeHTTPProxy}
			input.Executions = []Execution{cached}
		}, want: StatusComplete},
		{name: "partial failed channel", mutate: func(input *EnvelopeInput) {
			input.RequiredChannelIDs = []string{"channel_primary", "channel_failed"}
			input.Executions = []Execution{completed, failed}
		}, want: StatusPartial},
		{name: "partial error", mutate: func(input *EnvelopeInput) {
			input.Executions = []Execution{completed}
			input.Errors = []Error{{Code: ErrorUpstream, Message: "upstream failed"}}
		}, want: StatusPartial},
		{name: "partial truncated coverage", mutate: func(input *EnvelopeInput) {
			input.Executions = []Execution{completed}
			input.Coverage = []Coverage{{Truncated: true}}
		}, want: StatusPartial},
		{name: "partial explicit coverage gap", mutate: func(input *EnvelopeInput) {
			input.Executions = []Execution{completed}
			input.Coverage = []Coverage{{Exhaustive: &gap}}
		}, want: StatusPartial},
		{name: "partial fallback", mutate: func(input *EnvelopeInput) {
			fallback := completed
			fallback.Selection = "fallback"
			input.Executions = []Execution{fallback}
		}, want: StatusPartial},
		{name: "failed without completed channel", mutate: func(input *EnvelopeInput) {
			input.RequiredChannelIDs = []string{"channel_failed"}
			input.Executions = []Execution{failed, skipped}
		}, want: StatusFailed},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.mutate(&input)
			envelope, err := BuildEnvelope(input)
			if err != nil {
				t.Fatal(err)
			}
			if envelope.Status != test.want {
				t.Fatalf("BuildEnvelope() status = %q, want %q", envelope.Status, test.want)
			}
			if envelope.SchemaVersion != SchemaVersion || envelope.Meta.DurationMS != 1500 || envelope.Meta.ResultCount != len(input.Items) {
				t.Fatalf("BuildEnvelope() metadata = %#v", envelope.Meta)
			}
		})
	}

	// nil 公共数组在 JSON 中必须稳定为 []，避免各出口各自做空值修补。
	input := base
	input.Executions = []Execution{completed}
	envelope, err := BuildEnvelope(input)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"executions":[`, `"items":[]`, `"coverage":[]`, `"errors":[]`, `"limitations":[]`} {
		if !strings.Contains(string(raw), field) {
			t.Fatalf("BuildEnvelope() JSON = %s, want %s", raw, field)
		}
	}
	if envelope.Continuation.Mode != "none" {
		t.Fatalf("BuildEnvelope() continuation mode = %q, want none", envelope.Continuation.Mode)
	}
}

func TestBuildEnvelopeRejectsInvalidInput(t *testing.T) {
	started := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	completed := Execution{
		ChannelID: "channel_primary", Status: ExecutionCompleted, Selection: SelectionPrimary, StartedAt: started,
		Egress: &ExecutionEgress{ProfileID: "egress_environment", Mode: EgressModeEnvironment},
	}
	base := EnvelopeInput{
		RequestID:          "req_123e4567-e89b-42d3-a456-426614174000",
		Request:            validSearchOperation(),
		RequiredChannelIDs: []string{"channel_primary"},
		Executions:         []Execution{completed},
		StartedAt:          started,
		FinishedAt:         started.Add(time.Second),
	}

	cases := []struct {
		name   string
		mutate func(*EnvelopeInput)
	}{
		{name: "invalid request", mutate: func(input *EnvelopeInput) { input.Request.Query = nil }},
		{name: "invalid request ID", mutate: func(input *EnvelopeInput) { input.RequestID = "req_predictable" }},
		{name: "missing started timestamp", mutate: func(input *EnvelopeInput) { input.StartedAt = time.Time{} }},
		{name: "reversed timestamps", mutate: func(input *EnvelopeInput) { input.FinishedAt = started.Add(-time.Nanosecond) }},
		{name: "unknown execution status", mutate: func(input *EnvelopeInput) { input.Executions[0].Status = "unknown" }},
		{name: "unknown execution selection", mutate: func(input *EnvelopeInput) { input.Executions[0].Selection = "fallbak" }},
		{name: "completed without start", mutate: func(input *EnvelopeInput) { input.Executions[0].StartedAt = time.Time{} }},
		{name: "negative execution duration", mutate: func(input *EnvelopeInput) { input.Executions[0].DurationMS = -1 }},
		{name: "missing execution egress", mutate: func(input *EnvelopeInput) { input.Executions[0].Egress = nil }},
		{name: "failed execution freshness", mutate: func(input *EnvelopeInput) {
			freshUntil := started.Add(time.Hour)
			input.Executions[0].Status = ExecutionFailed
			input.Executions[0].FreshUntil = &freshUntil
		}},
		{name: "timed skipped execution", mutate: func(input *EnvelopeInput) {
			input.Executions[0].Status = ExecutionSkipped
			input.Executions[0].StartedAt = started
		}},
		{name: "missing required execution", mutate: func(input *EnvelopeInput) {
			input.RequiredChannelIDs = []string{"channel_primary", "channel_missing"}
		}},
		{name: "duplicate required channel", mutate: func(input *EnvelopeInput) {
			input.RequiredChannelIDs = []string{"channel_primary", "channel_primary"}
		}},
		{name: "unknown error code", mutate: func(input *EnvelopeInput) {
			input.Errors = []Error{{Code: "mystery"}}
		}},
		{name: "non-empty continuation", mutate: func(input *EnvelopeInput) {
			token := "cursor"
			input.Continuation = Continuation{Token: &token, Mode: "opaque"}
		}},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := base
			input.Executions = append([]Execution(nil), base.Executions...)
			test.mutate(&input)
			if _, err := BuildEnvelope(input); !errors.Is(err, ErrInvalidEnvelope) {
				t.Fatalf("BuildEnvelope() error = %v, want ErrInvalidEnvelope", err)
			}
		})
	}
}

func validSearchOperation() Operation {
	query := "agent search"
	return Operation{
		SchemaVersion:      SchemaVersion,
		Operation:          OperationSearch,
		Query:              &query,
		Scope:              Scope{Sources: []string{"github"}},
		RoutePolicy:        RoutePolicy{Mode: RouteAuto, AllowFallback: true},
		Limit:              20,
		IdentityDedupe:     IdentityExact,
		SimilarityGrouping: SimilarityOff,
		DeadlineMS:         30000,
	}
}

func replace(operation Operation, mutate func(*Operation)) Operation {
	mutate(&operation)
	return operation
}
