package adapter

import (
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/ylxmf2005/omnihub/internal/core"
)

func emptyOfficialSearchResult() core.AdapterResult {
	return core.AdapterResult{Items: []core.Item{}, Coverage: []core.Coverage{}, Errors: []core.Error{}, Limitations: []string{}, ProviderState: map[string]string{"auth_used": "false", "egress_proxied": "false"}}
}

func officialSearchFailure(channel core.Channel, template core.RouteTemplate, result core.AdapterResult, code core.ErrorCode, message string, retryable bool, details map[string]any) core.AdapterResult {
	result.Items = []core.Item{}
	result.Coverage = []core.Coverage{}
	result.Errors = []core.Error{{Code: code, Message: message, Source: channel.Source, Provider: template.Provider, ChannelID: channel.ID, RouteTemplateID: template.RouteTemplateID, Retryable: retryable, Details: details}}
	return result
}

func officialSearchLoopbackURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("invalid loopback URL")
	}
	address := net.ParseIP(parsed.Hostname())
	if address == nil || !address.IsLoopback() {
		return nil, errors.New("not loopback")
	}
	return parsed, nil
}

func quotedSearchText(value string) string {
	return `"` + strings.ReplaceAll(strings.TrimSpace(value), `"`, `\"`) + `"`
}

func containsContentField(fields []core.SearchContentField, target core.SearchContentField) bool {
	for _, field := range fields {
		if field == target {
			return true
		}
	}
	return false
}
