package semantic

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/egress"
	"github.com/ylxmf2005/omnihub/internal/registry"
	"github.com/ylxmf2005/omnihub/internal/repository"
)

const (
	maxInputBytes    = 8 << 10
	maxResponseBytes = 32 << 20
	groupPrefix      = "sem_"
)

var ErrUnavailable = errors.New("semantic grouping unavailable")

// Service 复用现有 Store 与可信 Egress；它不拥有模型进程，也不会选择或
// 回退 Endpoint。一次 Group 只处理 Query 已确定的最终 Item 顺序。
type Service struct {
	Store repository.Store
	Now   func() time.Time
}

// Preflight 在任何内容 Provider 执行前验证 semantic 的全部静态依赖。
// egress.Build 只构造 transport，不会发出请求。
func (service Service) Preflight(catalog *registry.Catalog, operation core.Operation) error {
	if operation.SimilarityGrouping != core.SimilaritySemantic {
		return nil
	}
	_, err := service.resolve(catalog, operation)
	return err
}

// Group 保留 Item 数量和顺序。embedding 或 cache 失败只使受影响 Item
// 保持 similarity=off，并通过一个脱敏问题把 Envelope 降为 partial。
func (service Service) Group(ctx context.Context, catalog *registry.Catalog, operation core.Operation, items []core.Item) ([]core.Item, *core.Error) {
	result := items
	resolved, err := service.resolve(catalog, operation)
	if err != nil {
		return result, nil
	}
	if len(result) == 0 {
		return result, nil
	}

	now := service.Now
	if now == nil {
		now = time.Now
	}
	records, keys, inputs, invalidInputs := prepareInputs(resolved, result)
	failure := groupFailure{reason: "invalid_input"}
	if invalidInputs == 0 {
		failure.reason = ""
	}

	// Cache hit 也重新验证维度、finite 与范数；损坏的向量绝不进入 cosine。
	hits, cacheErr := service.Store.GetEmbeddings(ctx, keys, now().UTC())
	if cacheErr != nil {
		return result, resolved.problem("cache_read_failed", len(result), false, false)
	}
	vectors := make(map[repository.EmbeddingCacheKey][]float32, len(records))
	invalidKeys := make(map[repository.EmbeddingCacheKey]bool)
	for key, entry := range hits {
		if entry.Key != key || !validVector(entry.Vector, resolved.profile.Dimension) {
			invalidKeys[key] = true
			failure.add("invalid_cache", false, false)
			continue
		}
		vectors[key] = entry.Vector
	}

	// 同一规范化输入只向 embedding Provider 发送一次；响应 index 仍按这一
	// 稳定 first-seen 顺序解释，重复 Item 在分组阶段共享向量。
	misses := make([]repository.EmbeddingCacheKey, 0, len(keys))
	missInputs := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := vectors[key]; ok || invalidKeys[key] {
			continue
		}
		misses = append(misses, key)
		missInputs = append(missInputs, inputs[key])
	}
	if len(misses) > 0 {
		fetched, proxied, requestFailure := requestEmbeddings(ctx, resolved, missInputs)
		if requestFailure != nil {
			failure.add(requestFailure.reason, requestFailure.retryable, requestFailure.proxied)
		} else {
			createdAt := now().UTC()
			entries := make([]repository.EmbeddingCacheEntry, len(misses))
			for index, key := range misses {
				entries[index] = repository.EmbeddingCacheEntry{
					Key: key, Vector: fetched[index], CreatedAt: createdAt, LastUsedAt: createdAt,
				}
			}
			if err := service.Store.PutEmbeddings(ctx, entries); err != nil {
				failure.add("cache_write_failed", false, proxied)
			} else {
				for index, key := range misses {
					vectors[key] = fetched[index]
				}
			}
		}
	}

	groupItems(result, records, vectors, resolved.profile)
	failedItems := invalidInputs
	for _, record := range records {
		if record.valid {
			if _, ok := vectors[record.key]; !ok {
				failedItems++
			}
		}
	}
	if failedItems == 0 {
		return result, nil
	}
	if failure.reason == "" {
		failure.reason = "embedding_unavailable"
	}
	return result, resolved.problem(failure.reason, failedItems, failure.retryable, failure.proxied)
}

type dependencies struct {
	profile          core.SemanticProfile
	endpoint         core.EndpointProfile
	egress           core.EgressProfile
	egressCredential *core.Credential
	credential       *core.Credential
	target           *url.URL
}

func (service Service) resolve(catalog *registry.Catalog, operation core.Operation) (dependencies, error) {
	if catalog == nil || service.Store == nil {
		return dependencies{}, fmt.Errorf("%w: catalog and embedding store are required", ErrUnavailable)
	}
	if operation.SemanticProfileID == nil || strings.TrimSpace(*operation.SemanticProfileID) == "" {
		return dependencies{}, fmt.Errorf("%w: semantic_profile_id is required", core.ErrInvalidOperation)
	}
	profile, ok := catalog.SemanticProfile(*operation.SemanticProfileID)
	if !ok || !profile.Enabled || profile.Validate() != nil {
		return dependencies{}, fmt.Errorf("%w: semantic profile is unavailable", ErrUnavailable)
	}
	endpoint, ok := catalog.Endpoint(profile.EndpointProfileID)
	if !ok || !endpoint.Enabled || endpoint.Provider != "embedding" || endpoint.EgressProfileID == "" {
		return dependencies{}, fmt.Errorf("%w: embedding endpoint is unavailable", ErrUnavailable)
	}
	target, err := embeddingURL(endpoint.BaseURL)
	if err != nil {
		return dependencies{}, fmt.Errorf("%w: embedding endpoint is invalid", ErrUnavailable)
	}
	egressProfile, ok := catalog.EgressProfile(endpoint.EgressProfileID)
	if !ok || !egressProfile.Enabled {
		return dependencies{}, fmt.Errorf("%w: embedding egress is unavailable", ErrUnavailable)
	}
	if egress.ValidateHTTPSOrDirectLoopback(target, egressProfile) != nil {
		return dependencies{}, fmt.Errorf("%w: embedding endpoint transport is unsafe", ErrUnavailable)
	}
	var egressCredential *core.Credential
	if egressProfile.CredentialID != "" {
		credential, exists := catalog.Credential(egressProfile.CredentialID)
		if !exists {
			return dependencies{}, fmt.Errorf("%w: embedding egress credential is unavailable", ErrUnavailable)
		}
		egressCredential = &credential
	}
	if _, err := egress.Build(egressProfile, egressCredential, nil); err != nil {
		return dependencies{}, fmt.Errorf("%w: embedding egress is invalid", ErrUnavailable)
	}

	var credential *core.Credential
	if profile.CredentialID != "" {
		resolved, exists := catalog.Credential(profile.CredentialID)
		if !exists || !validBearerCredential(resolved) || target.Scheme != "https" {
			return dependencies{}, fmt.Errorf("%w: embedding credential is unavailable", ErrUnavailable)
		}
		credential = &resolved
	}
	return dependencies{
		profile: profile, endpoint: endpoint, egress: egressProfile,
		egressCredential: egressCredential, credential: credential, target: target,
	}, nil
}

func validBearerCredential(credential core.Credential) bool {
	return credential.Revision > 0 && credential.Enabled && credential.Provider == "embedding" && credential.AuthKind == "bearer" && credential.Value != nil &&
		*credential.Value != "" && !strings.ContainsFunc(*credential.Value, unicode.IsControl)
}

func embeddingURL(raw string) (*url.URL, error) {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return nil, errors.New("base URL is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.IsAbs() == false || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("base URL must be an absolute HTTP URL")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1/embeddings"
	parsed.RawPath = ""
	return parsed, nil
}

type itemInput struct {
	key   repository.EmbeddingCacheKey
	valid bool
}

func prepareInputs(resolved dependencies, items []core.Item) ([]itemInput, []repository.EmbeddingCacheKey, map[repository.EmbeddingCacheKey]string, int) {
	records := make([]itemInput, len(items))
	keys := make([]repository.EmbeddingCacheKey, 0, len(items))
	inputs := make(map[repository.EmbeddingCacheKey]string, len(items))
	credentialID := ""
	credentialRevision := int64(0)
	if resolved.credential != nil {
		credentialID = resolved.credential.ID
		credentialRevision = resolved.credential.Revision
	}
	invalid := 0
	for index, item := range items {
		input, ok := embeddingInput(item)
		if !ok {
			invalid++
			continue
		}
		digest := sha256.Sum256([]byte(input))
		inputHash := hex.EncodeToString(digest[:])
		key := repository.EmbeddingCacheKey{
			InputHash: inputHash, EndpointProfileID: resolved.endpoint.ID, EndpointRevision: resolved.endpoint.Revision,
			CredentialID: credentialID, CredentialRevision: credentialRevision,
			Provider: resolved.endpoint.Provider, Model: resolved.profile.Model, Dimension: resolved.profile.Dimension,
			IndexRevision: resolved.profile.IndexRevision,
		}
		records[index] = itemInput{key: key, valid: true}
		if _, exists := inputs[key]; !exists {
			keys = append(keys, key)
			inputs[key] = input
		}
	}
	return records, keys, inputs, invalid
}

func embeddingInput(item core.Item) (string, bool) {
	title := normalizeNewlines(strings.TrimSpace(item.Title))
	body := ""
	if item.Summary != nil {
		body = *item.Summary
	} else if item.Content.Text != nil {
		body = *item.Content.Text
	}
	body = normalizeNewlines(body)
	if title == "" && body == "" {
		return "", false
	}
	return truncateUTF8(title+"\n\n"+body, maxInputBytes), true
}

func normalizeNewlines(value string) string {
	return strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(value)
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Data []embeddingData `json:"data"`
}

type embeddingData struct {
	Index     *int      `json:"index"`
	Embedding []float64 `json:"embedding"`
}

type providerFailure struct {
	reason    string
	retryable bool
	proxied   bool
}

func requestEmbeddings(ctx context.Context, resolved dependencies, inputs []string) ([][]float32, bool, *providerFailure) {
	encoded, err := json.Marshal(embeddingRequest{Model: resolved.profile.Model, Input: inputs})
	if err != nil {
		return nil, false, &providerFailure{reason: "request_encode_failed"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, resolved.target.String(), bytes.NewReader(encoded))
	if err != nil {
		return nil, false, &providerFailure{reason: "request_build_failed"}
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "OmniHub/1.0")
	if resolved.credential != nil {
		request.Header.Set("Authorization", "Bearer "+*resolved.credential.Value)
	}

	trusted, err := egress.Build(resolved.egress, resolved.egressCredential, nil)
	if err != nil {
		return nil, false, &providerFailure{reason: "egress_unavailable"}
	}
	client, err := trusted.HTTPClient()
	if err != nil {
		return nil, false, &providerFailure{reason: "egress_unavailable"}
	}
	defer client.CloseIdleConnections()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		retryable := true
		reason := "request_failed"
		if ctx.Err() != nil {
			reason = "request_timeout"
		}
		return nil, trusted.Proxied(), &providerFailure{reason: reason, retryable: retryable, proxied: trusted.Proxied()}
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		retryable := response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError
		return nil, trusted.Proxied(), &providerFailure{reason: "upstream_http_error", retryable: retryable, proxied: trusted.Proxied()}
	}
	if response.ContentLength > maxResponseBytes {
		return nil, trusted.Proxied(), &providerFailure{reason: "response_too_large", proxied: trusted.Proxied()}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, trusted.Proxied(), &providerFailure{reason: "response_read_failed", retryable: true, proxied: trusted.Proxied()}
	}
	if len(body) > maxResponseBytes {
		return nil, trusted.Proxied(), &providerFailure{reason: "response_too_large", proxied: trusted.Proxied()}
	}
	vectors, err := decodeEmbeddings(body, len(inputs), resolved.profile.Dimension)
	if err != nil {
		return nil, trusted.Proxied(), &providerFailure{reason: "invalid_response", proxied: trusted.Proxied()}
	}
	return vectors, trusted.Proxied(), nil
}

func decodeEmbeddings(raw []byte, count, dimension int) ([][]float32, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var document embeddingResponse
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	if len(document.Data) != count {
		return nil, errors.New("embedding response count mismatch")
	}
	vectors := make([][]float32, count)
	seen := make([]bool, count)
	for _, entry := range document.Data {
		if entry.Index == nil || *entry.Index < 0 || *entry.Index >= count || seen[*entry.Index] || len(entry.Embedding) != dimension {
			return nil, errors.New("embedding response index or dimension mismatch")
		}
		vector := make([]float32, dimension)
		for index, component := range entry.Embedding {
			if math.IsNaN(component) || math.IsInf(component, 0) {
				return nil, errors.New("embedding response contains a non-finite component")
			}
			vector[index] = float32(component)
		}
		if !validVector(vector, dimension) {
			return nil, errors.New("embedding response contains an invalid vector")
		}
		vectors[*entry.Index] = vector
		seen[*entry.Index] = true
	}
	for _, present := range seen {
		if !present {
			return nil, errors.New("embedding response misses an index")
		}
	}
	return vectors, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("embedding response contains extra JSON")
		}
		return err
	}
	return nil
}

func validVector(vector []float32, dimension int) bool {
	if len(vector) != dimension {
		return false
	}
	norm := 0.0
	for _, component := range vector {
		value := float64(component)
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
		norm += value * value
	}
	return norm > 0 && !math.IsInf(norm, 0)
}

func groupItems(items []core.Item, records []itemInput, vectors map[repository.EmbeddingCacheKey][]float32, profile core.SemanticProfile) {
	type representative struct {
		vector []float32
		group  string
	}
	representatives := make([]representative, 0, len(items))
	strategy := "semantic:" + profile.ID + ":" + profile.Model
	for index := range items {
		record := records[index]
		vector, ok := vectors[record.key]
		if !record.valid || !ok {
			continue
		}

		// ponytail: O(n²·d) is deliberate for <=100 final Items; spike the
		// existing sqlite-vec path at ~10k/cohort, p95>150ms, or cross-Snapshot KNN.
		bestIndex, bestScore := -1, -2.0
		for representativeIndex, candidate := range representatives {
			score := cosine(vector, candidate.vector)
			if score >= profile.Threshold && score > bestScore {
				bestIndex, bestScore = representativeIndex, score
			}
		}
		if bestIndex < 0 {
			group := semanticGroupID(profile.ID, record.key)
			representatives = append(representatives, representative{vector: vector, group: group})
			score := 1.0
			items[index].Similarity = core.Similarity{GroupID: &group, Strategy: strategy, Score: &score}
			continue
		}
		group := representatives[bestIndex].group
		score := clamp(bestScore)
		items[index].Similarity = core.Similarity{GroupID: &group, Strategy: strategy, Score: &score}
	}
}

func cosine(left, right []float32) float64 {
	dot, leftNorm, rightNorm := 0.0, 0.0, 0.0
	for index := range left {
		leftValue, rightValue := float64(left[index]), float64(right[index])
		dot += leftValue * rightValue
		leftNorm += leftValue * leftValue
		rightNorm += rightValue * rightValue
	}
	return clamp(dot / math.Sqrt(leftNorm*rightNorm))
}

func clamp(value float64) float64 {
	return min(1, max(-1, value))
}

func semanticGroupID(profileID string, representative repository.EmbeddingCacheKey) string {
	seed := strings.Join([]string{
		profileID, representative.InputHash, representative.EndpointProfileID,
		fmt.Sprintf("%d", representative.EndpointRevision), representative.CredentialID,
		fmt.Sprintf("%d", representative.CredentialRevision), representative.Provider,
		representative.Model, fmt.Sprintf("%d", representative.Dimension),
		fmt.Sprintf("%d", representative.IndexRevision),
	}, "\x00")
	digest := sha256.Sum256([]byte(seed))
	return groupPrefix + hex.EncodeToString(digest[:])
}

type groupFailure struct {
	reason    string
	retryable bool
	proxied   bool
}

func (failure *groupFailure) add(reason string, retryable, proxied bool) {
	if failure.reason == "" {
		failure.reason = reason
	} else if failure.reason != reason {
		failure.reason = "multiple_failures"
	}
	failure.retryable = failure.retryable || retryable
	failure.proxied = failure.proxied || proxied
}

func (resolved dependencies) problem(reason string, failedItems int, retryable, proxied bool) *core.Error {
	return &core.Error{
		Code: core.ErrorSimilarityUnavailable, Message: "semantic grouping is unavailable for some items",
		Provider: resolved.endpoint.Provider, Retryable: retryable,
		Details: map[string]any{
			"semantic_profile_id": resolved.profile.ID,
			"endpoint_profile_id": resolved.endpoint.ID,
			"model":               resolved.profile.Model,
			"egress_profile_id":   resolved.egress.ID,
			"egress_mode":         resolved.egress.Mode,
			"proxied":             proxied,
			"reason":              reason,
			"failed_items":        failedItems,
		},
	}
}
