// Package health 编排用户显式触发的 Channel Probe，并持久化有界健康事实。
package health

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/ylxmf2005/omnihub/internal/adapter"
	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/registry"
)

// RouteGroupKey 把除 Egress 外会改变一次 Probe 语义的配置收敛成稳定分组。
// Credential 只进入 ID 与 revision，值本身绝不会进入摘要材料。
func RouteGroupKey(catalog *registry.Catalog, channel core.Channel) (string, error) {
	if catalog == nil {
		return "", fmt.Errorf("health route group requires a catalog")
	}
	template, ok := catalog.RouteTemplate(channel.RouteTemplateID)
	if !ok {
		return "", fmt.Errorf("health route group requires a route template")
	}
	target, err := probeTarget(catalog, channel, template)
	if err != nil {
		return "", err
	}
	parameters, err := json.Marshal(channel.Parameters)
	if err != nil {
		return "", fmt.Errorf("encode health route parameters: %w", err)
	}
	parametersDigest := sha256.Sum256(parameters)

	credentialID, credentialRevision := channel.CredentialID, int64(0)
	if credentialID != "" {
		credential, exists := catalog.Credential(credentialID)
		if !exists {
			return "", fmt.Errorf("health route credential is unavailable")
		}
		credentialRevision = credential.Revision
	}
	material := struct {
		Source             string `json:"source"`
		RouteTemplateID    string `json:"route_template_id"`
		Target             string `json:"target"`
		ParametersHash     string `json:"parameters_hash"`
		CredentialID       string `json:"credential_id"`
		CredentialRevision int64  `json:"credential_revision"`
	}{
		Source: channel.Source, RouteTemplateID: template.RouteTemplateID, Target: target,
		ParametersHash: fmt.Sprintf("%x", parametersDigest), CredentialID: credentialID, CredentialRevision: credentialRevision,
	}
	encoded, err := json.Marshal(material)
	if err != nil {
		return "", fmt.Errorf("encode health route group: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("route_%x", digest), nil
}

func probeTarget(catalog *registry.Catalog, channel core.Channel, template core.RouteTemplate) (string, error) {
	switch template.Adapter {
	case "feed":
		raw, ok := channel.Parameters["url"].(string)
		if !ok {
			return "", fmt.Errorf("feed health route requires a URL")
		}
		normalized, err := adapter.NormalizeFeedURL(raw)
		if err != nil {
			return "", fmt.Errorf("normalize feed health target: %w", err)
		}
		parsed, _ := url.Parse(normalized)
		query, err := url.ParseQuery(parsed.RawQuery)
		if err != nil {
			return "", fmt.Errorf("normalize feed health query: %w", err)
		}
		parsed.RawQuery = query.Encode()
		return parsed.String(), nil
	case "rsshub":
		endpoint, ok := catalog.Endpoint(channel.EndpointProfileID)
		if !ok {
			return "", fmt.Errorf("RSSHub health route requires an endpoint")
		}
		target, err := adapter.BuildRSSHubFeedURL(endpoint, channel)
		if err != nil {
			return "", fmt.Errorf("normalize RSSHub health target: %w", err)
		}
		return target, nil
	default:
		return "", fmt.Errorf("adapter %q does not support layered Channel Probe", template.Adapter)
	}
}
