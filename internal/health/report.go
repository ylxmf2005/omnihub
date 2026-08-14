package health

import (
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"github.com/ylxmf2005/omnihub/internal/adapter"
	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/egress"
)

// feedHealthReport 刻意不包含 AdapterResult.Items、Content、Coverage 或
// ProviderState。健康历史只保存诊断事实，不成为第二份内容归档。
type feedHealthReport struct {
	CheckedAt time.Time            `json:"checked_at"`
	Egress    core.ExecutionEgress `json:"egress"`
	Checks    []egress.Check       `json:"checks"`
	Readiness string               `json:"readiness"`
	Result    feedHealthResult     `json:"result"`
}

type feedHealthResult struct {
	HTTP        httpHealthFacts `json:"http"`
	Feed        feedHealthFacts `json:"feed"`
	Errors      []core.Error    `json:"errors"`
	Limitations []string        `json:"limitations"`
}

type httpHealthFacts struct {
	Status      int    `json:"status,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

type feedHealthFacts struct {
	Parsed         bool       `json:"parsed"`
	Type           string     `json:"type,omitempty"`
	LatestItemTime *time.Time `json:"latest_item_time,omitempty"`
}

type rssHubHealthReport struct {
	CheckedAt time.Time              `json:"checked_at"`
	Egress    core.ExecutionEgress   `json:"egress"`
	Checks    []egress.Check         `json:"checks"`
	Readiness string                 `json:"readiness"`
	Endpoint  rssHubHTTPHealthFacts  `json:"endpoint"`
	Metadata  rssHubRouteHealthFacts `json:"metadata"`
	Feed      rssHubFeedHealthFacts  `json:"feed"`
}

type rssHubHTTPHealthFacts struct {
	Status      int         `json:"status,omitempty"`
	ContentType string      `json:"content_type,omitempty"`
	Passed      bool        `json:"passed"`
	Error       *core.Error `json:"error,omitempty"`
}

type rssHubRouteHealthFacts struct {
	Status      int         `json:"status,omitempty"`
	ContentType string      `json:"content_type,omitempty"`
	Passed      bool        `json:"passed"`
	RouteFound  bool        `json:"route_found"`
	Error       *core.Error `json:"error,omitempty"`
}

type rssHubFeedHealthFacts struct {
	Status         int         `json:"status,omitempty"`
	ContentType    string      `json:"content_type,omitempty"`
	FeedType       string      `json:"feed_type,omitempty"`
	FeedParsed     bool        `json:"feed_parsed"`
	LatestItemTime *time.Time  `json:"latest_item_time,omitempty"`
	Error          *core.Error `json:"error,omitempty"`
}

func projectFeedReport(report adapter.FeedProbeReport) (json.RawMessage, error) {
	latest := latestItemTime(report.Result.Items)
	status := feedHTTPStatus(report.Result)
	passed := len(report.Result.Errors) == 0 && len(report.Result.Coverage) > 0
	projection := feedHealthReport{
		CheckedAt: report.CheckedAt.UTC(), Egress: report.Egress, Checks: projectChecks(report.Checks),
		Readiness: "failed",
		Result: feedHealthResult{
			HTTP:   httpHealthFacts{Status: status, ContentType: report.Result.ProviderState["content_type"]},
			Feed:   feedHealthFacts{Parsed: passed, Type: report.Result.ProviderState["feed_type"], LatestItemTime: latest},
			Errors: projectErrors(report.Result.Errors), Limitations: projectLimitations(report.Result.Limitations),
		},
	}
	if passed {
		projection.Readiness = "ready"
	}
	return json.Marshal(projection)
}

func feedHTTPStatus(result core.AdapterResult) int {
	if status, err := strconv.Atoi(result.ProviderState["http_status"]); err == nil && status >= 100 && status <= 599 {
		return status
	}
	for _, problem := range result.Errors {
		var status int
		switch value := problem.Details["status"].(type) {
		case int:
			status = value
		case float64:
			status = int(value)
		case json.Number:
			status, _ = strconv.Atoi(string(value))
		}
		if status >= 100 && status <= 599 {
			return status
		}
	}
	return 0
}

func projectRSSHubReport(report adapter.RSSHubProbeReport) (json.RawMessage, error) {
	projection := rssHubHealthReport{
		CheckedAt: report.CheckedAt.UTC(), Egress: report.Egress, Checks: projectChecks(report.Checks), Readiness: report.Readiness,
		Endpoint: rssHubHTTPHealthFacts{
			Status: report.Endpoint.Status, ContentType: report.Endpoint.ContentType,
			Passed: report.Endpoint.Passed, Error: projectError(report.Endpoint.Error),
		},
		Metadata: rssHubRouteHealthFacts{
			Status: report.Metadata.Status, ContentType: report.Metadata.ContentType,
			Passed: report.Metadata.Passed, RouteFound: report.Metadata.RouteFound, Error: projectError(report.Metadata.Error),
		},
		Feed: rssHubFeedHealthFacts{
			Status: report.Feed.Status, ContentType: report.Feed.ContentType, FeedType: report.Feed.FeedType,
			FeedParsed: report.Feed.FeedParsed, LatestItemTime: cloneTime(report.Feed.LatestItemTime), Error: projectError(report.Feed.Error),
		},
	}
	return json.Marshal(projection)
}

func projectChecks(checks []egress.Check) []egress.Check {
	projected := make([]egress.Check, len(checks))
	for index, check := range checks {
		projected[index] = check
		projected[index].ResolvedIPs = slices.Clone(check.ResolvedIPs)
		if check.Subject == egress.SubjectProxy {
			// Profile ID、mode 与是否经过代理足以解释出口；代理地址和解析 IP
			// 仍只存在于用户配置与本次内存执行边界。
			projected[index].Address = ""
			projected[index].ResolvedIPs = nil
		}
	}
	return projected
}

func projectErrors(problems []core.Error) []core.Error {
	projected := make([]core.Error, 0, len(problems))
	for index := range problems {
		if problem := projectError(&problems[index]); problem != nil {
			projected = append(projected, *problem)
		}
	}
	return projected
}

func projectLimitations(limitations []string) []string {
	if limitations == nil {
		return []string{}
	}
	return slices.Clone(limitations)
}

func projectError(problem *core.Error) *core.Error {
	if problem == nil {
		return nil
	}
	projected := *problem
	// Probe health 不需要 Provider 的任意 details；HTTP status 已有专门 facts，
	// 丢弃 details 同时阻止未来 Adapter 把响应片段或网络地址带入历史记录。
	projected.Details = nil
	if projected.ValidateForStorage() != nil {
		projected.RetryAfterMS = nil
	}
	if projected.ValidateForStorage() != nil {
		return &core.Error{Code: core.ErrorProtocol, Message: "Probe error was redacted", Retryable: false}
	}
	return &projected
}

func latestItemTime(items []core.Item) *time.Time {
	var latest *time.Time
	for _, item := range items {
		observed := item.PublishedAt
		if observed == nil {
			observed = item.ModifiedAt
		}
		if observed != nil && (latest == nil || observed.After(*latest)) {
			value := observed.UTC()
			latest = &value
		}
	}
	return latest
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}
