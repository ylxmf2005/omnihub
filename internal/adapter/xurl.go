package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ylxmf2005/omnihub/internal/core"
	"golang.org/x/net/http/httpproxy"
)

const (
	DefaultMaxXURLOutputBytes int64 = 2 << 20
	maxXURLExecutionDuration        = 120 * time.Second
)

var errXURLOutputTooLarge = errors.New("xurl command output exceeds the configured limit")

// XURLRequest 是官方 xurl recent search 的完整执行边界。Credential 只会被
// 写入本次调用的临时 HOME，不进入 argv、环境变量或任何返回字段。
type XURLRequest struct {
	Operation     core.Operation
	Channel       core.Channel
	RouteTemplate core.RouteTemplate
	Credential    *core.Credential
	Egress        core.EgressProfile
}

// XURLAdapter 固定执行官方 xurl 的 app-only 认证与 recent search shortcut。
// Executable 只用于选择用户已安装并信任的二进制；为空时从当前 PATH 查找 xurl。
type XURLAdapter struct {
	Executable     string
	Now            func() time.Time
	MaxOutputBytes int64
}

type xurlSearchDocument struct {
	Data     []xurlTweet       `json:"data"`
	Includes xurlIncludes      `json:"includes"`
	Meta     xurlSearchMeta    `json:"meta"`
	Errors   []json.RawMessage `json:"errors"`
}

type xurlTweet struct {
	ID            string           `json:"id"`
	Text          string           `json:"text"`
	AuthorID      string           `json:"author_id"`
	CreatedAt     string           `json:"created_at"`
	PublicMetrics map[string]int64 `json:"public_metrics"`
	Entities      xurlEntities     `json:"entities"`
}

type xurlEntities struct {
	Hashtags []struct {
		Tag string `json:"tag"`
	} `json:"hashtags"`
}

type xurlIncludes struct {
	Users []xurlUser `json:"users"`
}

type xurlUser struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

type xurlSearchMeta struct {
	ResultCount *int   `json:"result_count"`
	NextToken   string `json:"next_token"`
}

type xurlCommandResult struct {
	stdout   []byte
	stderr   []byte
	err      error
	overflow bool
}

// Execute 不复用真实 HOME，也不在失败时尝试其他出口。xurl 自身不能提供
// 分层网络事实；egress_proxied 只表达固定 API 目标经过 child 环境计算的选择。
func (adapter XURLAdapter) Execute(ctx context.Context, request XURLRequest) (final core.AdapterResult) {
	result := emptyXURLResult()
	if ctx == nil {
		return xurlFailure(request, result, core.ErrorInternal, "xurl execution requires a context", false, nil)
	}
	if code, message := validateXURLRequest(request); code != "" {
		return xurlFailure(request, result, code, message, false, nil)
	}

	executable, err := adapter.executablePath()
	if err != nil {
		return xurlFailure(request, result, core.ErrorConfig, "xurl executable is unavailable", false, map[string]any{"reason": "dependency_unavailable"})
	}
	token := *request.Credential.Value
	home, err := os.MkdirTemp("", "omnihub-xurl-*")
	if err != nil {
		return xurlFailure(request, result, core.ErrorInternal, "create isolated xurl home", false, nil)
	}
	if err := os.Chmod(home, 0o700); err != nil {
		_ = os.RemoveAll(home)
		return xurlFailure(request, result, core.ErrorInternal, "protect isolated xurl home", false, nil)
	}
	defer func() {
		if err := os.RemoveAll(home); err != nil {
			final = xurlFailure(request, emptyXURLResult(), core.ErrorInternal, "remove isolated xurl credentials", false, nil)
		}
	}()

	duration := maxXURLExecutionDuration
	if request.Operation.DeadlineMS > 0 && request.Operation.DeadlineMS < int(maxXURLExecutionDuration/time.Millisecond) {
		duration = time.Duration(request.Operation.DeadlineMS) * time.Millisecond
	}
	executionContext, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	environment := xurlEnvironment(home, executable, request.Egress)
	proxied, err := xurlProxyDecision(environment, request.Egress.Mode)
	if err != nil {
		return xurlFailure(request, result, core.ErrorConfig, "xurl proxy environment is invalid", false, nil)
	}
	result.ProviderState["egress_proxied"] = strconv.FormatBool(proxied)

	// xurl 的 store 会读取 HOME 并可能导入 ~/.twurlrc。先在全新的 0700 HOME
	// 中从 stdin 写入 app-only token，随后两次命令始终复用这个隔离目录。
	auth := adapter.run(executionContext, executable, []string{"auth", "app-only", "-"}, environment, home, token+"\n")
	if xurlOutputContainsCredential(auth, token) {
		return xurlFailure(request, result, core.ErrorProtocol, "xurl output exposed credential material", false, nil)
	}
	if auth.overflow {
		return xurlFailure(request, result, core.ErrorProtocol, errXURLOutputTooLarge.Error(), false, map[string]any{"limit_bytes": adapter.maxOutputBytes()})
	}
	if auth.err != nil {
		if contextOperationFailed(executionContext, auth.err) {
			return xurlFailure(request, result, core.ErrorTimeout, "xurl credential setup timed out", true, nil)
		}
		return xurlFailure(request, result, core.ErrorAuth, "xurl could not prepare app-only authentication", false, xurlExitDetails(auth.err))
	}

	query := strings.TrimSpace(*request.Operation.Query)
	search := adapter.run(executionContext, executable, []string{"search", "--max-results", strconv.Itoa(request.Operation.Limit), "--auth", "app", "--", query}, environment, home, "")
	if xurlOutputContainsCredential(search, token) {
		return xurlFailure(request, result, core.ErrorProtocol, "xurl output exposed credential material", false, nil)
	}
	if search.overflow {
		return xurlFailure(request, result, core.ErrorProtocol, errXURLOutputTooLarge.Error(), false, map[string]any{"limit_bytes": adapter.maxOutputBytes()})
	}
	if search.err != nil {
		if contextOperationFailed(executionContext, search.err) {
			return xurlFailure(request, result, core.ErrorTimeout, "xurl search timed out", true, nil)
		}
		if code, message, retryable, details, ok := classifyXURLAPIError(search.stdout); ok {
			result.ProviderState["auth_used"] = "true"
			return xurlFailure(request, result, code, message, retryable, details)
		}
		if len(bytes.TrimSpace(search.stdout)) > 0 && json.Valid(search.stdout) {
			result.ProviderState["auth_used"] = "true"
			return xurlFailure(request, result, core.ErrorProtocol, "xurl failed with an unrecognized JSON response", false, xurlExitDetails(search.err))
		}
		code, message, retryable := classifyXURLCommandError(search.stderr)
		result.ProviderState["auth_used"] = "true"
		return xurlFailure(request, result, code, message, retryable, xurlExitDetails(search.err))
	}

	result.ProviderState["auth_used"] = "true"
	return normalizeXURLSearch(request, result, search.stdout, adapter.now())
}

func validateXURLRequest(request XURLRequest) (core.ErrorCode, string) {
	if request.Operation.Operation != core.OperationSearch || request.Operation.Query == nil || strings.TrimSpace(*request.Operation.Query) == "" || strings.IndexByte(*request.Operation.Query, 0) >= 0 || request.Operation.Limit < 1 || request.Operation.Limit > 100 {
		return core.ErrorParameter, "xurl only supports a non-empty search with limit from 1 to 100"
	}
	if request.RouteTemplate.RouteTemplateID == "" || request.Channel.RouteTemplateID != request.RouteTemplate.RouteTemplateID || request.RouteTemplate.Provider != "xurl" || request.RouteTemplate.Adapter != "xurl" {
		return core.ErrorConfig, "RouteTemplate is not bound to xurl"
	}
	if request.Channel.ID == "" || request.Channel.Source == "" || request.Channel.EndpointProfileID != "" || request.Channel.EgressProfileID == "" || request.Channel.EgressProfileID != request.Egress.ID || len(request.Channel.Parameters) != 0 {
		return core.ErrorConfig, "xurl Channel binding is invalid"
	}
	if !request.Egress.Enabled || request.Egress.Validate() != nil || request.Egress.CredentialID != "" {
		return core.ErrorConfig, "xurl egress configuration is invalid"
	}
	switch request.Egress.Mode {
	case core.EgressModeDirect, core.EgressModeEnvironment, core.EgressModeHTTPProxy:
	case core.EgressModeSOCKS5:
		return core.ErrorConfig, "xurl does not support SOCKS5 egress"
	default:
		return core.ErrorConfig, "xurl egress configuration is invalid"
	}
	credential := request.Credential
	if request.Channel.CredentialID == "" || credential == nil || credential.ID != request.Channel.CredentialID || credential.Provider != "xurl" || credential.AuthKind != "app_only" || !credential.Enabled || credential.Value == nil || !validXURLToken(*credential.Value) {
		return core.ErrorAuth, "xurl app-only credential is unavailable"
	}
	return "", ""
}

func validXURLToken(token string) bool {
	return token != "" && len(token) <= 8192 && token == strings.TrimSpace(token) && strings.IndexFunc(token, func(character rune) bool {
		return unicode.IsControl(character) || unicode.IsSpace(character)
	}) < 0
}

func (adapter XURLAdapter) executablePath() (string, error) {
	name := adapter.Executable
	if name == "" {
		name = "xurl"
	}
	if name != strings.TrimSpace(name) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", errors.New("invalid xurl executable")
	}
	resolved, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

func xurlEnvironment(home, executable string, profile core.EgressProfile) []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = filepath.Dir(executable)
	}
	environment := []string{"HOME=" + home, "PATH=" + path, "NO_COLOR=1"}
	switch profile.Mode {
	case core.EgressModeEnvironment:
		for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
			if value, ok := os.LookupEnv(name); ok {
				environment = append(environment, name+"="+value)
			}
		}
	case core.EgressModeHTTPProxy:
		environment = append(environment, "HTTPS_PROXY="+profile.ProxyEndpoint, "https_proxy="+profile.ProxyEndpoint)
	}
	return environment
}

func xurlProxyDecision(environment []string, mode core.EgressMode) (bool, error) {
	switch mode {
	case core.EgressModeDirect:
		return false, nil
	case core.EgressModeHTTPProxy:
		return true, nil
	}
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = value
		}
	}
	config := httpproxy.Config{
		HTTPProxy:  firstNonEmpty(values["HTTP_PROXY"], values["http_proxy"]),
		HTTPSProxy: firstNonEmpty(values["HTTPS_PROXY"], values["https_proxy"]),
		NoProxy:    firstNonEmpty(values["NO_PROXY"], values["no_proxy"]),
	}
	target, _ := url.Parse("https://api.x.com")
	proxy, err := config.ProxyFunc()(target)
	return proxy != nil, err
}

func (adapter XURLAdapter) run(ctx context.Context, executable string, argv, environment []string, directory, stdin string) xurlCommandResult {
	commandContext, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := newXURLBoundedOutput(adapter.maxOutputBytes(), cancel)
	stderr := newXURLBoundedOutput(adapter.maxOutputBytes(), cancel)
	command := exec.CommandContext(commandContext, executable, argv...)
	command.Env = environment
	command.Dir = directory
	command.Stdout = stdout
	command.Stderr = stderr
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	err := command.Run()
	return xurlCommandResult{
		stdout:   bytes.Clone(stdout.Bytes()),
		stderr:   bytes.Clone(stderr.Bytes()),
		err:      err,
		overflow: stdout.Exceeded() || stderr.Exceeded(),
	}
}

type xurlBoundedOutput struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
	cancel   context.CancelFunc
}

func newXURLBoundedOutput(limit int64, cancel context.CancelFunc) *xurlBoundedOutput {
	return &xurlBoundedOutput{limit: int(limit), cancel: cancel}
}

func (output *xurlBoundedOutput) Write(value []byte) (int, error) {
	written := len(value)
	remaining := output.limit - output.buffer.Len()
	if remaining > 0 {
		_, _ = output.buffer.Write(value[:min(remaining, len(value))])
	}
	if len(value) > remaining && !output.exceeded {
		output.exceeded = true
		output.cancel()
	}
	return written, nil
}

func (output *xurlBoundedOutput) Bytes() []byte {
	return output.buffer.Bytes()
}

func (output *xurlBoundedOutput) Exceeded() bool {
	return output.exceeded
}

func (adapter XURLAdapter) maxOutputBytes() int64 {
	if adapter.MaxOutputBytes > 0 && adapter.MaxOutputBytes < DefaultMaxXURLOutputBytes {
		return adapter.MaxOutputBytes
	}
	return DefaultMaxXURLOutputBytes
}

func xurlOutputContainsCredential(result xurlCommandResult, credential string) bool {
	secret := []byte(credential)
	return bytes.Contains(result.stdout, secret) || bytes.Contains(result.stderr, secret)
}

func classifyXURLAPIError(raw []byte) (core.ErrorCode, string, bool, map[string]any, bool) {
	var document struct {
		Status json.RawMessage `json:"status"`
		Title  string          `json:"title"`
		Errors []struct {
			Code json.RawMessage `json:"code"`
		} `json:"errors"`
	}
	if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &document) != nil {
		return "", "", false, nil, false
	}
	status, _ := xurlJSONInteger(document.Status)
	apiCode := 0
	for _, problem := range document.Errors {
		if value, ok := xurlJSONInteger(problem.Code); ok {
			apiCode = value
			break
		}
	}
	if status == 0 {
		switch apiCode {
		case 32, 89, 99, 135, 215, 220:
			status = 401
		case 88:
			status = 429
		case 130, 131:
			status = 503
		case 400, 401, 403, 408, 429:
			status = apiCode
		default:
			if apiCode >= 500 && apiCode <= 599 {
				status = apiCode
			}
		}
	}
	if status == 0 {
		title := strings.ToLower(document.Title)
		switch {
		case strings.Contains(title, "too many") || strings.Contains(title, "rate limit"):
			status = 429
		case strings.Contains(title, "unauthorized") || strings.Contains(title, "forbidden"):
			status = 401
		case strings.Contains(title, "invalid") || strings.Contains(title, "bad request"):
			status = 400
		}
	}
	if status == 0 && len(document.Errors) == 0 && document.Title == "" {
		return "", "", false, nil, false
	}
	details := map[string]any{}
	if status != 0 {
		details["status"] = status
	}
	if apiCode != 0 && apiCode != status {
		details["api_code"] = apiCode
	}
	code, message, retryable := xurlHTTPFailure(status)
	return code, message, retryable, details, true
}

func xurlJSONInteger(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var number int
	if json.Unmarshal(raw, &number) == nil {
		return number, true
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return 0, false
	}
	number, err := strconv.Atoi(text)
	return number, err == nil
}

func xurlHTTPFailure(status int) (core.ErrorCode, string, bool) {
	switch status {
	case 400, 422:
		return core.ErrorParameter, "X API rejected the search parameters", false
	case 401, 403:
		return core.ErrorAuth, "X API rejected app-only authentication", false
	case 408:
		return core.ErrorTimeout, "X API search timed out", true
	case 429:
		return core.ErrorRateLimit, "X API rate limit exceeded", true
	default:
		if status >= 500 {
			return core.ErrorUpstream, "X API search is unavailable", true
		}
		return core.ErrorUpstream, "X API rejected the search", false
	}
}

func classifyXURLCommandError(stderr []byte) (core.ErrorCode, string, bool) {
	message := strings.ToLower(string(stderr))
	switch {
	case strings.Contains(message, "429") || strings.Contains(message, "rate limit"):
		return core.ErrorRateLimit, "X API rate limit exceeded", true
	case strings.Contains(message, "auth") || strings.Contains(message, "bearer") || strings.Contains(message, "token") || strings.Contains(message, "unauthorized") || strings.Contains(message, "forbidden"):
		return core.ErrorAuth, "xurl authentication failed", false
	case strings.Contains(message, "timeout") || strings.Contains(message, "deadline"):
		return core.ErrorTimeout, "xurl search timed out", true
	case strings.Contains(message, "dial tcp") || strings.Contains(message, "connection refused") || strings.Contains(message, "no such host") || strings.Contains(message, "network is unreachable") || strings.Contains(message, "tls handshake") || strings.Contains(message, "x509"):
		return core.ErrorNetwork, "xurl could not reach the X API", true
	case strings.Contains(message, "json"):
		return core.ErrorParse, "xurl could not parse the X API response", false
	default:
		return core.ErrorUpstream, "xurl search failed", false
	}
}

func xurlExitDetails(err error) map[string]any {
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return map[string]any{"exit_code": exitError.ExitCode()}
	}
	return nil
}

func normalizeXURLSearch(request XURLRequest, result core.AdapterResult, raw []byte, retrievedAt time.Time) core.AdapterResult {
	var root map[string]json.RawMessage
	if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &root) != nil {
		return xurlFailure(request, result, core.ErrorParse, "parse xurl search response", false, nil)
	}
	metaRaw, hasMeta := root["meta"]
	if !hasMeta {
		return xurlFailure(request, result, core.ErrorProtocol, "xurl search response is missing meta", false, nil)
	}
	var document xurlSearchDocument
	if json.Unmarshal(raw, &document) != nil {
		return xurlFailure(request, result, core.ErrorParse, "parse xurl search response", false, nil)
	}
	if bytes.Equal(bytes.TrimSpace(metaRaw), []byte("null")) || document.Meta.ResultCount == nil || *document.Meta.ResultCount < 0 {
		return xurlFailure(request, result, core.ErrorProtocol, "xurl search response contains invalid meta", false, nil)
	}
	dataRaw, hasData := root["data"]
	if hasData && bytes.Equal(bytes.TrimSpace(dataRaw), []byte("null")) || !hasData && *document.Meta.ResultCount != 0 || *document.Meta.ResultCount != len(document.Data) || len(document.Data) > 100 {
		return xurlFailure(request, result, core.ErrorProtocol, "xurl search response contains inconsistent data", false, nil)
	}
	if len(document.Errors) > 0 && len(document.Data) == 0 {
		return xurlFailure(request, result, core.ErrorProtocol, "xurl returned errors without a failed command", false, map[string]any{"error_count": len(document.Errors)})
	}

	users := make(map[string]xurlUser, len(document.Includes.Users))
	for _, user := range document.Includes.Users {
		if user.ID == "" || !validXUsername(user.Username) {
			return xurlFailure(request, result, core.ErrorProtocol, "xurl search response contains an invalid user", false, nil)
		}
		users[user.ID] = user
	}
	items := make([]core.Item, 0, len(document.Data))
	var from, to *time.Time
	for index, tweet := range document.Data {
		if !xurlNumericID(tweet.ID) {
			return xurlFailure(request, result, core.ErrorProtocol, "xurl search response contains an invalid post id", false, map[string]any{"result_index": index})
		}
		var publishedAt *time.Time
		if tweet.CreatedAt != "" {
			parsed, err := time.Parse(time.RFC3339Nano, tweet.CreatedAt)
			if err != nil {
				return xurlFailure(request, result, core.ErrorParse, "parse xurl post created_at", false, map[string]any{"result_index": index})
			}
			value := parsed.UTC()
			publishedAt = &value
			if from == nil || value.Before(*from) {
				copy := value
				from = &copy
			}
			if to == nil || value.After(*to) {
				copy := value
				to = &copy
			}
		}

		user, hasUser := users[tweet.AuthorID]
		postURL := "https://x.com/i/web/status/" + tweet.ID
		authors := []core.Author{}
		if hasUser {
			postURL = "https://x.com/" + user.Username + "/status/" + tweet.ID
			name := strings.TrimSpace(user.Name)
			if name == "" {
				name = "@" + user.Username
			}
			authorURL := "https://x.com/" + user.Username
			authors = append(authors, core.Author{Name: name, URL: &authorURL})
		}
		metrics := make(map[string]any, len(tweet.PublicMetrics))
		for name, value := range tweet.PublicMetrics {
			metrics[name] = value
		}
		if len(metrics) == 0 {
			metrics = nil
		}
		tags := make([]string, 0, len(tweet.Entities.Hashtags))
		for _, hashtag := range tweet.Entities.Hashtags {
			tags = append(tags, hashtag.Tag)
		}
		text := tweet.Text
		upstreamID := tweet.ID
		rank := index + 1
		limitations := []string{"x_recent_search_window", "xurl_shortcut_no_continuation"}
		items = append(items, core.Item{
			URL:         postURL,
			Content:     core.Content{Role: core.ContentBody, Text: &text, SourceSupplied: true},
			PublishedAt: publishedAt,
			Authors:     authors,
			Tags:        uniqueTrimmed(tags),
			Metrics:     metrics,
			Observations: []core.Observation{{
				Source: request.Channel.Source, Provider: "xurl", ChannelID: request.Channel.ID,
				RouteTemplateID: request.RouteTemplate.RouteTemplateID, UpstreamID: &upstreamID,
				OriginalURL: postURL, CanonicalURL: postURL, RetrievedAt: retrievedAt.UTC(), Rank: &rank,
				Verification: core.VerificationBody, Limitations: limitations,
			}},
		})
	}

	examined, returned, exhaustive := len(document.Data), len(items), false
	limitations := []string{"x_recent_search_window", "xurl_shortcut_no_continuation"}
	if len(document.Errors) > 0 {
		limitations = append(limitations, "x_partial_response")
		result.Errors = []core.Error{{
			Code: core.ErrorUpstream, Message: "X API returned partial result errors", Source: request.Channel.Source,
			Provider: "xurl", ChannelID: request.Channel.ID, RouteTemplateID: request.RouteTemplate.RouteTemplateID,
			Retryable: false, Details: map[string]any{"error_count": len(document.Errors)},
		}}
	} else {
		result.Errors = []core.Error{}
	}
	result.Items = items
	result.Coverage = []core.Coverage{{
		Source: request.Channel.Source, ChannelID: request.Channel.ID, RouteTemplateID: request.RouteTemplate.RouteTemplateID,
		Scope: "x_recent_search_window", From: from, To: to, Examined: &examined, Returned: &returned,
		Exhaustive: &exhaustive, Truncated: true, Limitations: limitations,
	}}
	result.Limitations = limitations
	return result
}

func xurlNumericID(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func validXUsername(value string) bool {
	if value == "" || len(value) > 15 {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func emptyXURLResult() core.AdapterResult {
	return core.AdapterResult{Items: []core.Item{}, Coverage: []core.Coverage{}, Errors: []core.Error{}, ProviderState: map[string]string{}}
}

func xurlFailure(request XURLRequest, result core.AdapterResult, code core.ErrorCode, message string, retryable bool, details map[string]any) core.AdapterResult {
	result.Items = []core.Item{}
	result.Coverage = []core.Coverage{}
	result.Errors = []core.Error{{
		Code: code, Message: message, Source: request.Channel.Source, Provider: "xurl", ChannelID: request.Channel.ID,
		RouteTemplateID: request.RouteTemplate.RouteTemplateID, Retryable: retryable, Details: details,
	}}
	return result
}

func (adapter XURLAdapter) now() time.Time {
	if adapter.Now != nil {
		return adapter.Now().UTC()
	}
	return time.Now().UTC()
}
