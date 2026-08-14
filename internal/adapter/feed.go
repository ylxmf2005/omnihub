package adapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mmcdole/gofeed"
	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/egress"
	"golang.org/x/net/html"
)

const DefaultMaxFeedResponseBytes int64 = 10 << 20

var (
	ErrInvalidFeedURL   = errors.New("invalid feed URL")
	errFeedBodyTooLarge = errors.New("feed response exceeds the configured limit")
)

// FeedRequest 把 Router 已选择的真实 Channel 与静态 RouteTemplate 一起交给
// Adapter。CacheKey 是可选的额外分区（例如 Credential revision），不会替代
// Channel、RouteTemplate 与参数本身的缓存分区。
type FeedRequest struct {
	Operation        core.Operation
	Channel          core.Channel
	RouteTemplate    core.RouteTemplate
	Egress           core.EgressProfile
	EgressCredential *core.Credential
	CacheKey         string
}

// FeedCacheEntry 只保存重新验证响应所需的有限上游事实。Body 在写入前已经通过
// gofeed 解析与响应大小检查，文件缓存因此不会保存任意未验证页面。
type FeedCacheEntry struct {
	Body           []byte    `json:"body"`
	ETag           string    `json:"etag,omitempty"`
	LastModified   string    `json:"last_modified,omitempty"`
	EffectiveURL   string    `json:"effective_url"`
	FeedType       string    `json:"feed_type"`
	StoredAt       time.Time `json:"stored_at"`
	FreshUntil     time.Time `json:"fresh_until,omitempty"`
	MustRevalidate bool      `json:"must_revalidate,omitempty"`
}

// FeedCache 同时适用于进程内与文件缓存；Adapter 不依赖具体持久层。
type FeedCache interface {
	Get(context.Context, string) (FeedCacheEntry, bool, error)
	Put(context.Context, string, FeedCacheEntry) error
}

type feedCacheDeleter interface {
	Delete(context.Context, string) error
}

type MemoryFeedCache struct {
	mu      sync.RWMutex
	entries map[string]FeedCacheEntry
}

func NewMemoryFeedCache() *MemoryFeedCache {
	return &MemoryFeedCache{entries: make(map[string]FeedCacheEntry)}
}

func (cache *MemoryFeedCache) Get(ctx context.Context, key string) (FeedCacheEntry, bool, error) {
	if err := ctx.Err(); err != nil {
		return FeedCacheEntry{}, false, err
	}
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	entry, ok := cache.entries[key]
	entry.Body = bytes.Clone(entry.Body)
	return entry, ok, nil
}

func (cache *MemoryFeedCache) Put(ctx context.Context, key string, entry FeedCacheEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries == nil {
		cache.entries = make(map[string]FeedCacheEntry)
	}
	entry.Body = bytes.Clone(entry.Body)
	cache.entries[key] = entry
	return nil
}

func (cache *MemoryFeedCache) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	delete(cache.entries, key)
	return nil
}

// FileFeedCache 用 key 的摘要作为文件名，避免 Channel 参数进入路径。写入先落
// 同目录临时文件再 rename；Unix 保持原子替换，Windows 因标准库限制采用
// remove + rename，但无论哪一端都不会把半写入 JSON 当作已提交缓存。
type FileFeedCache struct {
	Directory string
	mu        sync.Mutex
}

func NewFileFeedCache(directory string) *FileFeedCache {
	return &FileFeedCache{Directory: directory}
}

func (cache *FileFeedCache) Get(ctx context.Context, key string) (FeedCacheEntry, bool, error) {
	if err := ctx.Err(); err != nil {
		return FeedCacheEntry{}, false, err
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	path, err := cache.path(key)
	if err != nil {
		return FeedCacheEntry{}, false, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return FeedCacheEntry{}, false, nil
	}
	if err != nil {
		return FeedCacheEntry{}, false, fmt.Errorf("read feed cache: %w", err)
	}
	defer file.Close()
	raw, err := readBounded(file, DefaultMaxFeedResponseBytes*2)
	if err != nil {
		return FeedCacheEntry{}, false, fmt.Errorf("read bounded feed cache: %w", err)
	}
	var entry FeedCacheEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return FeedCacheEntry{}, false, fmt.Errorf("decode feed cache: %w", err)
	}
	return entry, true, nil
}

func (cache *FileFeedCache) Put(ctx context.Context, key string, entry FeedCacheEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	path, err := cache.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cache.Directory, 0o700); err != nil {
		return fmt.Errorf("create feed cache directory: %w", err)
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode feed cache: %w", err)
	}
	temporary, err := os.CreateTemp(cache.Directory, ".feed-cache-*")
	if err != nil {
		return fmt.Errorf("create temporary feed cache: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write feed cache: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close feed cache: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		// Windows 的 Rename 不会替换现有目标。缓存内容没有持久业务状态，
		// 因此在该平台退化为 remove + rename；同 key 的进程内写仍由 mutex
		// 串行，跨进程竞争则由最后一次完整 rename 胜出。
		if runtime.GOOS != "windows" {
			return fmt.Errorf("commit feed cache: %w", err)
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("replace feed cache: %w", removeErr)
		}
		if renameErr := os.Rename(temporaryPath, path); renameErr != nil {
			return fmt.Errorf("commit feed cache: %w", renameErr)
		}
	}
	committed = true
	return nil
}

func (cache *FileFeedCache) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	path, err := cache.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete feed cache: %w", err)
	}
	return nil
}

func (cache *FileFeedCache) path(key string) (string, error) {
	if strings.TrimSpace(cache.Directory) == "" {
		return "", errors.New("feed cache directory is required")
	}
	digest := sha256.Sum256([]byte(key))
	return filepath.Join(cache.Directory, fmt.Sprintf("%x.json", digest)), nil
}

// FeedCacheKey 通过 JSON 的确定性 map key 编码把 Channel、RouteTemplate、参数
// 和调用方的额外分区收敛为固定 key，避免参数变化复用旧响应。
func FeedCacheKey(request FeedRequest) (string, error) {
	material := struct {
		ChannelID       string         `json:"channel_id"`
		RouteTemplateID string         `json:"route_template_id"`
		Parameters      map[string]any `json:"parameters"`
		EgressID        string         `json:"egress_id,omitempty"`
		EgressRevision  int64          `json:"egress_revision,omitempty"`
		CredentialID    string         `json:"egress_credential_id,omitempty"`
		CredentialRev   int64          `json:"egress_credential_revision,omitempty"`
		Partition       string         `json:"partition,omitempty"`
	}{
		ChannelID:       request.Channel.ID,
		RouteTemplateID: routeTemplateID(request),
		Parameters:      request.Channel.Parameters,
		EgressID:        request.Egress.ID,
		EgressRevision:  request.Egress.Revision,
		Partition:       request.CacheKey,
	}
	if request.EgressCredential != nil {
		material.CredentialID = request.EgressCredential.ID
		material.CredentialRev = request.EgressCredential.Revision
	}
	raw, err := json.Marshal(material)
	if err != nil {
		return "", fmt.Errorf("encode feed cache key: %w", err)
	}
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("feed:%x", digest), nil
}

// NormalizeFeedURL 是执行前可复用的纯校验入口。Direct Feed 不允许凭据进入
// URL，因此除 http(s) 与无 userinfo 外，也拒绝常见 secret query/fragment key。
func NormalizeFeedURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return "", fmt.Errorf("%w: an absolute http(s) URL is required", ErrInvalidFeedURL)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("%w: URL userinfo is not allowed", ErrInvalidFeedURL)
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", fmt.Errorf("%w: query is malformed", ErrInvalidFeedURL)
	}
	for key := range query {
		if core.CredentialLikeURLKey(key) {
			return "", fmt.Errorf("%w: secret-like query key %q is not allowed", ErrInvalidFeedURL, key)
		}
	}
	// OAuth 风格 fragment 可能与 query 一样携带 token。普通页面 anchor
	// 不带 key=value，仍可在规范化时安全移除。
	if strings.Contains(parsed.Fragment, "=") {
		fragment, fragmentErr := url.ParseQuery(strings.TrimPrefix(parsed.Fragment, "?"))
		if fragmentErr != nil {
			return "", fmt.Errorf("%w: fragment is malformed", ErrInvalidFeedURL)
		}
		for key := range fragment {
			if core.CredentialLikeURLKey(key) {
				return "", fmt.Errorf("%w: secret-like fragment key %q is not allowed", ErrInvalidFeedURL, key)
			}
		}
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if parsed.Scheme == "http" && port == "80" || parsed.Scheme == "https" && port == "443" {
		port = ""
	}
	if strings.Contains(hostname, ":") {
		if port == "" {
			parsed.Host = "[" + hostname + "]"
		} else {
			parsed.Host = net.JoinHostPort(hostname, port)
		}
	} else if port == "" {
		parsed.Host = hostname
	} else {
		parsed.Host = net.JoinHostPort(hostname, port)
	}
	parsed.Fragment = ""
	return parsed.String(), nil
}

type FeedAdapter struct {
	Cache            FeedCache
	Now              func() time.Time
	MaxResponseBytes int64
	rssHubCredential *rssHubCredentialPolicy
	trusted          *egress.Client
	probe            *egress.Probe
}

type FeedProbeReport struct {
	CheckedAt time.Time            `json:"checked_at"`
	Egress    core.ExecutionEgress `json:"egress"`
	Checks    []egress.Check       `json:"checks"`
	Result    core.AdapterResult   `json:"result"`
}

func (adapter FeedAdapter) Execute(ctx context.Context, request FeedRequest) (final core.AdapterResult) {
	result := emptyFeedResult()
	defer func() {
		annotateFeedEgress(&final, request.Egress, adapter.trusted)
	}()
	if ctx == nil {
		return feedFailureResult(request, result, core.ErrorInternal, "feed execution requires a context", false, nil, nil)
	}
	now := adapter.now()
	result.ProviderState["cache_status"] = "disabled"

	if request.RouteTemplate.RouteTemplateID != "" && request.Channel.RouteTemplateID != "" && request.RouteTemplate.RouteTemplateID != request.Channel.RouteTemplateID {
		return feedFailureResult(request, result, core.ErrorConfig, "feed Channel and RouteTemplate do not match", false, nil, nil)
	}
	if request.RouteTemplate.Adapter != "" && request.RouteTemplate.Adapter != "feed" {
		return feedFailureResult(request, result, core.ErrorConfig, "RouteTemplate is not bound to the feed adapter", false, nil, nil)
	}
	rawURL, ok := request.Channel.Parameters["url"].(string)
	if !ok || strings.TrimSpace(rawURL) == "" {
		return feedFailureResult(request, result, core.ErrorConfig, "feed Channel requires a string url parameter", false, nil, nil)
	}
	configuredURL, err := NormalizeFeedURL(rawURL)
	if err != nil {
		return feedFailureResult(request, result, core.ErrorConfig, "feed Channel URL is invalid", false, nil, nil)
	}
	result.ProviderState["effective_url"] = configuredURL
	if request.Egress.ID == "" {
		return feedFailureResult(request, result, core.ErrorConfig, "feed execution requires an explicit egress profile", false, nil, nil)
	}
	adapter.trusted, err = egress.Build(request.Egress, request.EgressCredential, adapter.probe)
	if err != nil {
		return feedFailureResult(request, result, core.ErrorConfig, "feed egress configuration is invalid", false, nil, nil)
	}

	cacheKey, err := FeedCacheKey(request)
	if err != nil {
		return feedFailureResult(request, result, core.ErrorConfig, "feed Channel parameters cannot form a cache key", false, nil, nil)
	}
	var cached FeedCacheEntry
	var cacheFound bool
	if adapter.Cache != nil {
		result.ProviderState["cache_status"] = "miss"
		cached, cacheFound, err = adapter.Cache.Get(ctx, cacheKey)
		if err != nil {
			if contextOperationFailed(ctx, err) {
				return feedFailureResult(request, result, core.ErrorTimeout, "feed execution timed out while reading cache", true, nil, nil)
			}
			return feedFailureResult(request, result, core.ErrorInternal, "read feed response cache", false, nil, nil)
		}
		if cacheFound {
			cachedURL, cacheURLErr := NormalizeFeedURL(cached.EffectiveURL)
			if cacheURLErr != nil || len(cached.Body) == 0 || int64(len(cached.Body)) > adapter.maxResponseBytes() || adapter.rssHubCredential != nil && adapter.rssHubCredential.cacheEntryContainsSensitive(cachedURL, cached) {
				adapter.discardCache(ctx, cacheKey)
				cacheFound = false
			} else {
				cached.EffectiveURL = cachedURL
			}
		}
	}

	// 新鲜缓存不访问网络，但仍重新解析受限 body；这让内存与文件缓存共享完全
	// 相同的规范化路径，也不会把缓存序列化格式变成第二套 Item 合同。
	if cacheFound && !cached.MustRevalidate && cached.FreshUntil.After(now) {
		feed, parseErr := parseFeed(cached.Body)
		if parseErr == nil {
			result.ProviderState["cache_status"] = "hit"
			result.ProviderState["effective_url"] = cached.EffectiveURL
			result.ProviderState["feed_type"] = feed.FeedType
			freshUntil := cached.FreshUntil.UTC()
			result.FreshUntil = &freshUntil
			return normalizeFeedResult(request, result, feed, cached.EffectiveURL, now)
		}
		adapter.discardCache(ctx, cacheKey)
		cacheFound = false
	}

	requestURL := configuredURL
	etag, lastModified := "", ""
	if cacheFound {
		requestURL = cached.EffectiveURL
		etag, lastModified = cached.ETag, cached.LastModified
		result.ProviderState["cache_status"] = "stale"
	}
	response, failure := adapter.fetch(ctx, requestURL, etag, lastModified)
	if failure != nil {
		return feedFailureResult(request, result, failure.Code, failure.Message, failure.Retryable, failure.RetryAfterMS, failure.Details)
	}
	result.ProviderState["http_status"] = strconv.Itoa(response.Status)
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		mediaType, _, _ := mime.ParseMediaType(contentType)
		result.ProviderState["content_type"] = mediaType
	}

	var body []byte
	var effectiveURL string
	var responseHeader http.Header
	var revalidated bool
	if response.NotModified {
		if !cacheFound {
			return feedFailureResult(request, result, core.ErrorProtocol, "upstream returned 304 without a cached feed", true, nil, map[string]any{"status": http.StatusNotModified})
		}
		body, effectiveURL, responseHeader, revalidated = cached.Body, cached.EffectiveURL, response.Header, true
		result.ProviderState["cache_status"] = "revalidated"
	} else {
		body, effectiveURL, responseHeader = response.Body, response.EffectiveURL, response.Header
		if cacheFound {
			result.ProviderState["cache_status"] = "refreshed"
		}

		// HTML discovery 只跟随一个明确的 alternate Feed。alternate 自身若仍是
		// HTML，会以 protocol_error 终止，避免无界页面发现链。
		if responseIsHTML(responseHeader, body) {
			if adapter.rssHubCredential != nil {
				return feedFailureResult(request, result, core.ErrorProtocol, "credentialed RSSHub responses cannot use HTML feed discovery", false, nil, nil)
			}
			alternate, discoveryErr := discoverAlternateFeed(body, effectiveURL)
			if discoveryErr != nil {
				return feedFailureResult(request, result, core.ErrorProtocol, "upstream HTML does not expose a valid alternate feed", false, nil, nil)
			}
			discovered, discoveredFailure := adapter.fetch(ctx, alternate, "", "")
			if discoveredFailure != nil {
				return feedFailureResult(request, result, discoveredFailure.Code, discoveredFailure.Message, discoveredFailure.Retryable, discoveredFailure.RetryAfterMS, discoveredFailure.Details)
			}
			if discovered.NotModified {
				return feedFailureResult(request, result, core.ErrorProtocol, "alternate feed returned 304 without a cached representation", true, nil, map[string]any{"status": http.StatusNotModified})
			}
			if responseIsHTML(discovered.Header, discovered.Body) {
				return feedFailureResult(request, result, core.ErrorProtocol, "alternate feed resolved to HTML", false, nil, nil)
			}
			body, effectiveURL, responseHeader = discovered.Body, discovered.EffectiveURL, discovered.Header
		}
	}
	result.ProviderState["effective_url"] = effectiveURL

	parseStarted := time.Now()
	feed, err := parseFeed(body)
	if adapter.probe != nil {
		adapter.probe.RecordFeedParse(err, parseStarted)
	}
	if err != nil {
		return feedFailureResult(request, result, core.ErrorParse, "parse upstream feed", false, nil, nil)
	}
	result.ProviderState["feed_type"] = feed.FeedType
	policy := deriveCachePolicy(responseHeader, body, now, cached, revalidated)
	if !policy.noStore && !policy.freshUntil.IsZero() {
		freshUntil := policy.freshUntil.UTC()
		result.FreshUntil = &freshUntil
	}

	if adapter.Cache != nil {
		if policy.noStore {
			adapter.discardCache(ctx, cacheKey)
			result.ProviderState["cache_status"] = "bypass"
		} else {
			etag, lastModified := responseHeader.Get("ETag"), responseHeader.Get("Last-Modified")
			if revalidated {
				etag = firstNonEmpty(etag, cached.ETag)
				lastModified = firstNonEmpty(lastModified, cached.LastModified)
			}
			entry := FeedCacheEntry{
				Body:           bytes.Clone(body),
				ETag:           etag,
				LastModified:   lastModified,
				EffectiveURL:   effectiveURL,
				FeedType:       feed.FeedType,
				StoredAt:       now,
				FreshUntil:     policy.freshUntil,
				MustRevalidate: policy.mustRevalidate,
			}
			if err := adapter.Cache.Put(ctx, cacheKey, entry); err != nil {
				if contextOperationFailed(ctx, err) {
					return feedFailureResult(request, result, core.ErrorTimeout, "feed execution timed out while writing cache", true, nil, nil)
				}
				return feedFailureResult(request, result, core.ErrorInternal, "write feed response cache", false, nil, nil)
			}
		}
	}
	return normalizeFeedResult(request, result, feed, effectiveURL, now)
}

type feedHTTPResponse struct {
	Body         []byte
	Header       http.Header
	EffectiveURL string
	NotModified  bool
	Status       int
}

type feedRequestFailure struct {
	Code         core.ErrorCode
	Message      string
	Retryable    bool
	RetryAfterMS *int
	Details      map[string]any
}

func (adapter FeedAdapter) fetch(ctx context.Context, target, etag, lastModified string) (feedHTTPResponse, *feedRequestFailure) {
	normalized, err := NormalizeFeedURL(target)
	if err != nil {
		return feedHTTPResponse{}, &feedRequestFailure{Code: core.ErrorProtocol, Message: "upstream selected an invalid feed URL"}
	}
	if adapter.probe != nil {
		ctx = adapter.probe.Context(ctx)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, normalized, nil)
	if err != nil {
		return feedHTTPResponse{}, &feedRequestFailure{Code: core.ErrorConfig, Message: "construct upstream feed request"}
	}
	request.Header.Set("Accept", "application/atom+xml, application/rss+xml, application/feed+json, application/json;q=0.9, text/html;q=0.5, */*;q=0.1")
	request.Header.Set("User-Agent", "OmniHub/1.0")
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		request.Header.Set("If-Modified-Since", lastModified)
	}

	response, err := adapter.httpClient().Do(request)
	if err != nil {
		if adapter.probe != nil {
			adapter.probe.RecordRequestError(err)
		}
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if errors.Is(err, errRSSHubExplicitEgressRequired) {
			return feedHTTPResponse{}, &feedRequestFailure{Code: core.ErrorConfig, Message: "credentialed RSSHub request requires an explicit egress profile"}
		}
		if errors.Is(err, egress.ErrCleartextCredential) || errors.Is(err, egress.ErrUntrustedTransport) {
			return feedHTTPResponse{}, &feedRequestFailure{Code: core.ErrorConfig, Message: "credentialed RSSHub egress is invalid"}
		}
		if errors.Is(err, ErrInvalidFeedURL) {
			return feedHTTPResponse{}, &feedRequestFailure{Code: core.ErrorProtocol, Message: "upstream redirect selected an invalid feed URL"}
		}
		var networkError net.Error
		if contextOperationFailed(ctx, err) || errors.As(err, &networkError) && networkError.Timeout() {
			return feedHTTPResponse{}, &feedRequestFailure{Code: core.ErrorTimeout, Message: "upstream feed request timed out", Retryable: true}
		}
		return feedHTTPResponse{}, &feedRequestFailure{Code: core.ErrorNetwork, Message: "request upstream feed", Retryable: true}
	}
	defer response.Body.Close()
	if adapter.probe != nil {
		adapter.probe.RecordHTTP(response.StatusCode)
	}
	if adapter.rssHubCredential != nil && adapter.rssHubCredential.containsSensitiveHeaders(response.Header) {
		return feedHTTPResponse{}, &feedRequestFailure{Code: core.ErrorProtocol, Message: "RSSHub response exposed access material"}
	}
	var effectiveURL string
	var normalizeErr error
	if adapter.rssHubCredential != nil {
		var cleanURL *url.URL
		cleanURL, normalizeErr = adapter.rssHubCredential.cleanURL(response.Request.URL)
		if normalizeErr == nil {
			effectiveURL = cleanURL.String()
		}
	} else {
		effectiveURL, normalizeErr = NormalizeFeedURL(response.Request.URL.String())
	}
	if normalizeErr != nil {
		return feedHTTPResponse{}, &feedRequestFailure{Code: core.ErrorProtocol, Message: "upstream response has an invalid effective URL"}
	}
	if response.StatusCode == http.StatusNotModified {
		return feedHTTPResponse{Header: response.Header.Clone(), EffectiveURL: effectiveURL, NotModified: true, Status: response.StatusCode}, nil
	}
	if response.StatusCode != http.StatusOK {
		code, retryable := core.ErrorProtocol, false
		switch response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			code, retryable = core.ErrorAuth, false
		case http.StatusTooManyRequests:
			code, retryable = core.ErrorRateLimit, true
		case http.StatusRequestTimeout:
			code, retryable = core.ErrorTimeout, true
		default:
			if response.StatusCode >= 500 {
				code, retryable = core.ErrorUpstream, true
			} else if response.StatusCode >= 400 {
				code, retryable = core.ErrorUpstream, false
			}
		}
		return feedHTTPResponse{}, &feedRequestFailure{
			Code:         code,
			Message:      fmt.Sprintf("upstream feed returned HTTP %d", response.StatusCode),
			Retryable:    retryable,
			RetryAfterMS: parseRetryAfter(response.Header.Get("Retry-After"), adapter.now()),
			Details:      map[string]any{"status": response.StatusCode},
		}
	}
	body, err := readBounded(response.Body, adapter.maxResponseBytes())
	if err != nil {
		if errors.Is(err, errFeedBodyTooLarge) {
			return feedHTTPResponse{}, &feedRequestFailure{Code: core.ErrorProtocol, Message: "upstream feed response exceeds the size limit", Details: map[string]any{"limit_bytes": adapter.maxResponseBytes()}}
		}
		var networkError net.Error
		if contextOperationFailed(ctx, err) || errors.As(err, &networkError) && networkError.Timeout() {
			return feedHTTPResponse{}, &feedRequestFailure{Code: core.ErrorTimeout, Message: "read upstream feed response timed out", Retryable: true}
		}
		return feedHTTPResponse{}, &feedRequestFailure{Code: core.ErrorNetwork, Message: "read upstream feed response", Retryable: true}
	}
	if adapter.rssHubCredential != nil && adapter.rssHubCredential.containsSensitive(string(body)) {
		return feedHTTPResponse{}, &feedRequestFailure{Code: core.ErrorProtocol, Message: "RSSHub response exposed access material"}
	}
	return feedHTTPResponse{Body: body, Header: response.Header.Clone(), EffectiveURL: effectiveURL, Status: response.StatusCode}, nil
}

func (adapter FeedAdapter) httpClient() *http.Client {
	var client *http.Client
	var err error
	if adapter.rssHubCredential == nil {
		client, err = adapter.trusted.HTTPClient()
	} else {
		var base http.RoundTripper
		base, err = adapter.trusted.RoundTripper()
		if err == nil {
			client = &http.Client{Transport: &rssHubCredentialTransport{base: base, policy: adapter.rssHubCredential, egress: adapter.trusted}}
		}
	}
	if err != nil {
		return &http.Client{Transport: rssHubCredentialTransportRejected{}}
	}
	return adapter.withRedirectPolicy(client)
}

func (adapter FeedAdapter) withRedirectPolicy(client *http.Client) *http.Client {
	originalCheck := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if adapter.rssHubCredential != nil {
			cleanURL, err := adapter.rssHubCredential.cleanURL(request.URL)
			if err != nil {
				return err
			}
			request.URL = cleanURL
			clearRSSHubRestrictedHeaders(request.Header)
		}
		if _, err := NormalizeFeedURL(request.URL.String()); err != nil {
			return err
		}
		if originalCheck != nil {
			return originalCheck(request, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return client
}

// Probe 明确执行一次无缓存 Feed 请求，并把该请求真实经过的网络层与 Feed
// 解析结果一起返回。它不会在 Execute 之外另做预检。
func (adapter FeedAdapter) Probe(ctx context.Context, request FeedRequest) FeedProbeReport {
	rawURL, _ := request.Channel.Parameters["url"].(string)
	normalized, _ := NormalizeFeedURL(rawURL)
	probe := egress.NewProbe(request.Egress, normalized)
	adapter.Cache = nil
	adapter.probe = probe
	result := adapter.Execute(ctx, request)
	if len(result.Errors) > 0 && result.Errors[0].Code == core.ErrorConfig {
		probe.RecordConfigurationError()
	}
	report := probe.Report()
	return FeedProbeReport{CheckedAt: report.CheckedAt, Egress: report.Egress, Checks: report.Checks, Result: result}
}

func annotateFeedEgress(result *core.AdapterResult, profile core.EgressProfile, client *egress.Client) {
	if result == nil || profile.ID == "" {
		return
	}
	result.ProviderState["egress_profile_id"] = profile.ID
	result.ProviderState["egress_mode"] = string(profile.Mode)
	result.ProviderState["egress_proxied"] = strconv.FormatBool(client != nil && client.Proxied())
}

func (adapter FeedAdapter) maxResponseBytes() int64 {
	if adapter.MaxResponseBytes > 0 && adapter.MaxResponseBytes < DefaultMaxFeedResponseBytes {
		return adapter.MaxResponseBytes
	}
	return DefaultMaxFeedResponseBytes
}

func (adapter FeedAdapter) now() time.Time {
	if adapter.Now != nil {
		return adapter.Now().UTC()
	}
	return time.Now().UTC()
}

func (adapter FeedAdapter) discardCache(ctx context.Context, key string) {
	if deleter, ok := adapter.Cache.(feedCacheDeleter); ok {
		_ = deleter.Delete(ctx, key)
		return
	}
	// 第三方 Cache 只实现最小 Get/Put 合同时，用空 tombstone 覆盖旧 body；
	// 后续读取会把它视为 miss，no-store 响应内容不会继续被复用。
	if adapter.Cache != nil {
		_ = adapter.Cache.Put(ctx, key, FeedCacheEntry{})
	}
}

func parseFeed(body []byte) (*gofeed.Feed, error) {
	feed, err := gofeed.NewParser().Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	switch feed.FeedType {
	case "rss", "atom", "json":
		return feed, nil
	default:
		return nil, errors.New("unsupported feed type")
	}
}

func normalizeFeedResult(request FeedRequest, result core.AdapterResult, feed *gofeed.Feed, effectiveURL string, retrievedAt time.Time) core.AdapterResult {
	items := make([]core.Item, 0, len(feed.Items))
	upstreamIDCounts := make(map[string]int)
	for _, upstream := range feed.Items {
		if upstream != nil && strings.TrimSpace(upstream.GUID) != "" {
			upstreamIDCounts[strings.TrimSpace(upstream.GUID)]++
		}
	}
	var windowFrom, windowTo *time.Time
	for index, upstream := range feed.Items {
		if upstream == nil {
			continue
		}
		duplicateUpstreamID := upstreamIDCounts[strings.TrimSpace(upstream.GUID)] > 1
		item := normalizeFeedItem(request, feed, upstream, effectiveURL, retrievedAt, index, duplicateUpstreamID)
		items = append(items, item)
		observedAt := item.PublishedAt
		if observedAt == nil {
			observedAt = item.ModifiedAt
		}
		if observedAt != nil {
			if windowFrom == nil || observedAt.Before(*windowFrom) {
				value := *observedAt
				windowFrom = &value
			}
			if windowTo == nil || observedAt.After(*windowTo) {
				value := *observedAt
				windowTo = &value
			}
		}
	}
	examined, returned, exhaustive := len(feed.Items), len(items), false
	result.Items = items
	result.Coverage = []core.Coverage{{
		Source:          request.Channel.Source,
		ChannelID:       request.Channel.ID,
		RouteTemplateID: routeTemplateID(request),
		Scope:           "upstream_feed_window",
		From:            windowFrom,
		To:              windowTo,
		Examined:        &examined,
		Returned:        &returned,
		Exhaustive:      &exhaustive,
		Truncated:       true,
		Limitations:     []string{"upstream_retention_unknown"},
	}}
	result.Errors = []core.Error{}
	result.Limitations = []string{"upstream_retention_unknown"}
	return result
}

func normalizeFeedItem(request FeedRequest, feed *gofeed.Feed, upstream *gofeed.Item, effectiveURL string, retrievedAt time.Time, index int, duplicateUpstreamID bool) core.Item {
	rawItemURL := strings.TrimSpace(upstream.Link)
	itemURL := resolveHTTPURL(rawItemURL, effectiveURL)
	if itemURL == "" && looksLikeHTTPURL(upstream.GUID) {
		itemURL = resolveHTTPURL(upstream.GUID, effectiveURL)
	}
	content, verification := normalizeFeedContent(upstream)
	summary := optionalTrimmed(upstream.Description)
	upstreamID := optionalTrimmed(upstream.GUID)
	rank := index + 1
	limitations := make([]string, 0, 2)
	if itemURL == "" {
		if rawItemURL == "" {
			limitations = append(limitations, "item_url_missing")
		} else {
			limitations = append(limitations, "item_url_invalid")
		}
	} else if rawItemURL != "" && resolveHTTPURL(rawItemURL, effectiveURL) == "" {
		limitations = append(limitations, "item_url_invalid")
	}
	if duplicateUpstreamID {
		limitations = append(limitations, "upstream_id_not_unique_in_feed")
	}

	authors := normalizeFeedAuthors(upstream.Authors)
	if len(authors) == 0 && upstream.Author != nil {
		authors = normalizeFeedAuthors([]*gofeed.Person{upstream.Author})
	}
	if len(authors) == 0 {
		authors = normalizeFeedAuthors(feed.Authors)
	}
	attachments := make([]core.Attachment, 0, len(upstream.Enclosures))
	for _, enclosure := range upstream.Enclosures {
		if enclosure == nil {
			continue
		}
		attachmentURL := resolveHTTPURL(enclosure.URL, effectiveURL)
		if attachmentURL == "" {
			limitations = appendUnique(limitations, "attachment_url_invalid")
			continue
		}
		attachments = append(attachments, core.Attachment{URL: attachmentURL, MIMEType: optionalTrimmed(enclosure.Type)})
	}
	var image *string
	if upstream.Image != nil {
		image = optionalTrimmed(resolveHTTPURL(upstream.Image.URL, effectiveURL))
	}

	return core.Item{
		URL:         itemURL,
		Title:       strings.TrimSpace(upstream.Title),
		Content:     content,
		Summary:     summary,
		Image:       image,
		PublishedAt: utcTime(upstream.PublishedParsed),
		ModifiedAt:  utcTime(upstream.UpdatedParsed),
		Authors:     authors,
		Tags:        uniqueTrimmed(upstream.Categories),
		Language:    optionalTrimmed(feed.Language),
		Attachments: attachments,
		Observations: []core.Observation{{
			Source:          request.Channel.Source,
			Provider:        feedProvider(request),
			ChannelID:       request.Channel.ID,
			RouteTemplateID: routeTemplateID(request),
			Endpoint:        effectiveURL,
			UpstreamID:      upstreamID,
			OriginalURL:     itemURL,
			CanonicalURL:    itemURL,
			RetrievedAt:     retrievedAt.UTC(),
			Rank:            &rank,
			Verification:    verification,
			Limitations:     limitations,
		}},
	}
}

func normalizeFeedContent(item *gofeed.Item) (core.Content, core.Verification) {
	if value := strings.TrimSpace(item.Content); value != "" {
		content := core.Content{Role: core.ContentBody, SourceSupplied: true}
		if containsHTML(value) {
			content.HTML = &value
		} else {
			content.Text = &value
		}
		return content, core.VerificationBody
	}
	if value := strings.TrimSpace(item.Description); value != "" {
		content := core.Content{Role: core.ContentSummary, SourceSupplied: true}
		if containsHTML(value) {
			content.HTML = &value
		} else {
			content.Text = &value
		}
		return content, core.VerificationMetadata
	}
	return core.Content{Role: core.ContentSnippet, SourceSupplied: false}, core.VerificationMetadata
}

func normalizeFeedAuthors(people []*gofeed.Person) []core.Author {
	authors := make([]core.Author, 0, len(people))
	seen := make(map[string]bool)
	for _, person := range people {
		if person == nil {
			continue
		}
		name := strings.TrimSpace(person.Name)
		if name == "" {
			name = strings.TrimSpace(person.Email)
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		authors = append(authors, core.Author{Name: name})
	}
	return authors
}

func emptyFeedResult() core.AdapterResult {
	return core.AdapterResult{
		Items:         []core.Item{},
		Coverage:      []core.Coverage{},
		Errors:        []core.Error{},
		ProviderState: map[string]string{},
	}
}

func feedFailureResult(request FeedRequest, result core.AdapterResult, code core.ErrorCode, message string, retryable bool, retryAfter *int, details map[string]any) core.AdapterResult {
	result.Items = []core.Item{}
	result.Coverage = []core.Coverage{}
	result.FreshUntil = nil
	result.Errors = []core.Error{{
		Code:            code,
		Message:         message,
		Source:          request.Channel.Source,
		Provider:        feedProvider(request),
		ChannelID:       request.Channel.ID,
		RouteTemplateID: routeTemplateID(request),
		Retryable:       retryable,
		RetryAfterMS:    retryAfter,
		Details:         details,
	}}
	return result
}

func routeTemplateID(request FeedRequest) string {
	if request.RouteTemplate.RouteTemplateID != "" {
		return request.RouteTemplate.RouteTemplateID
	}
	return request.Channel.RouteTemplateID
}

func feedProvider(request FeedRequest) string {
	if request.RouteTemplate.Provider != "" {
		return request.RouteTemplate.Provider
	}
	return "direct-feed"
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errFeedBodyTooLarge
	}
	return body, nil
}

func responseIsHTML(header http.Header, body []byte) bool {
	mediaType, _, _ := mime.ParseMediaType(header.Get("Content-Type"))
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		return true
	}
	prefix := strings.ToLower(strings.TrimSpace(string(body[:min(len(body), 512)])))
	return strings.HasPrefix(prefix, "<!doctype html") || strings.HasPrefix(prefix, "<html")
}

func discoverAlternateFeed(body []byte, baseURL string) (string, error) {
	document, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	var discovered string
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if discovered != "" {
			return
		}
		if node.Type == html.ElementNode && strings.EqualFold(node.Data, "link") {
			attributes := make(map[string]string, len(node.Attr))
			for _, attribute := range node.Attr {
				attributes[strings.ToLower(attribute.Key)] = strings.TrimSpace(attribute.Val)
			}
			if containsToken(attributes["rel"], "alternate") && alternateFeedMediaType(attributes["type"]) && attributes["href"] != "" {
				discovered = resolveHTTPURL(attributes["href"], baseURL)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(document)
	if discovered == "" {
		return "", errors.New("alternate feed link not found")
	}
	return NormalizeFeedURL(discovered)
}

func alternateFeedMediaType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	switch strings.ToLower(mediaType) {
	case "application/rss+xml", "application/atom+xml", "application/feed+json", "application/json":
		return true
	default:
		return false
	}
}

func containsToken(value, target string) bool {
	for _, token := range strings.Fields(strings.ToLower(value)) {
		if token == target {
			return true
		}
	}
	return false
}

type feedCachePolicy struct {
	freshUntil     time.Time
	mustRevalidate bool
	noStore        bool
}

func deriveCachePolicy(header http.Header, body []byte, now time.Time, previous FeedCacheEntry, revalidated bool) feedCachePolicy {
	policy := feedCachePolicy{}
	directives := parseCacheControl(header.Values("Cache-Control"))
	if directives["no-store"] != "" || hasDirective(directives, "no-store") {
		policy.noStore = true
		return policy
	}
	if _, ok := directives["no-cache"]; ok {
		policy.mustRevalidate = true
	}
	if raw, ok := directives["max-age"]; ok {
		if seconds, err := strconv.ParseInt(strings.Trim(raw, "\""), 10, 64); err == nil && seconds >= 0 {
			policy.freshUntil = addSecondsBounded(now, seconds)
		}
	} else if expires, err := http.ParseTime(header.Get("Expires")); err == nil {
		policy.freshUntil = expires.UTC()
	} else if revalidated && !previous.StoredAt.IsZero() {
		// 304 未更新 freshness header 时沿用原响应的 lifetime，从本次验证时刻
		// 重新计算；no-cache 则继续要求每次验证。
		lifetime := previous.FreshUntil.Sub(previous.StoredAt)
		if lifetime > 0 {
			policy.freshUntil = now.Add(lifetime)
		}
		policy.mustRevalidate = policy.mustRevalidate || previous.MustRevalidate
	} else if minutes := rssTTLMinutes(body); minutes > 0 {
		const maxDurationMinutes = int64((1<<63 - 1) / int64(time.Minute))
		if minutes > maxDurationMinutes {
			minutes = maxDurationMinutes
		}
		policy.freshUntil = now.Add(time.Duration(minutes) * time.Minute)
	}
	if policy.mustRevalidate {
		policy.freshUntil = now
	}
	return policy
}

func parseCacheControl(values []string) map[string]string {
	directives := make(map[string]string)
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			name, raw, found := strings.Cut(strings.TrimSpace(part), "=")
			name = strings.ToLower(strings.TrimSpace(name))
			if name == "" {
				continue
			}
			if found {
				directives[name] = strings.TrimSpace(raw)
			} else {
				directives[name] = ""
			}
		}
	}
	return directives
}

func hasDirective(directives map[string]string, key string) bool {
	_, ok := directives[key]
	return ok
}

func rssTTLMinutes(body []byte) int64 {
	var rss struct {
		Channel struct {
			TTL string `xml:"ttl"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(body, &rss); err != nil {
		return 0
	}
	minutes, err := strconv.ParseInt(strings.TrimSpace(rss.Channel.TTL), 10, 64)
	if err != nil || minutes <= 0 {
		return 0
	}
	return minutes
}

func addSecondsBounded(now time.Time, seconds int64) time.Time {
	const maxDurationSeconds = int64((1<<63 - 1) / int64(time.Second))
	if seconds > maxDurationSeconds {
		seconds = maxDurationSeconds
	}
	return now.Add(time.Duration(seconds) * time.Second)
}

func parseRetryAfter(raw string, now time.Time) *int {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil
	}
	var milliseconds int64
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		if seconds > int64(maxInt())/1000 {
			milliseconds = int64(maxInt())
		} else {
			milliseconds = seconds * 1000
		}
	} else if retryAt, err := http.ParseTime(value); err == nil {
		milliseconds = retryAt.Sub(now).Milliseconds()
		if milliseconds < 0 {
			milliseconds = 0
		}
	} else {
		return nil
	}
	result := int(milliseconds)
	return &result
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

func resolveHTTPURL(raw, base string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.User != nil {
		return ""
	}
	if !parsed.IsAbs() {
		baseURL, baseErr := url.Parse(base)
		if baseErr != nil {
			return ""
		}
		parsed = baseURL.ResolveReference(parsed)
	}
	if parsed.Host == "" || !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return ""
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Fragment = ""
	return parsed.String()
}

func looksLikeHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Host != "" && (strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https"))
}

func containsHTML(value string) bool {
	tokenizer := html.NewTokenizer(strings.NewReader(value))
	for {
		switch tokenizer.Next() {
		case html.StartTagToken, html.SelfClosingTagToken, html.EndTagToken:
			return true
		case html.ErrorToken:
			return false
		}
	}
}

func utcTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func optionalTrimmed(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func uniqueTrimmed(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		result = append(result, trimmed)
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func contextOperationFailed(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded)
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
