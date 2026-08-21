package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
	"unicode"
)

type nativeEvent struct {
	message wireMessage
	err     error
}

type clientRequest struct {
	message      wireMessage
	reply        chan wireMessage
	disconnected <-chan struct{}
}

type pendingRequest struct {
	client   clientRequest
	expected messageType
}

// RunHost 运行 Chrome Native Messaging stdio 与当前用户 IPC 之间的单活桥接，不读取或持久化浏览器数据。
func RunHost(ctx context.Context, stdin io.Reader, stdout io.Writer, runtimeDir string, now func() time.Time) (runErr error) {
	if stdin == nil || stdout == nil {
		return bridgeError(ErrorScopeInvalid, "native messaging stdin and stdout are required")
	}
	if now == nil {
		now = time.Now
	}
	nativeEvents := make(chan nativeEvent, 1)
	go readNativeMessages(stdin, nativeEvents)

	var first nativeEvent
	select {
	case <-ctx.Done():
		return ctx.Err()
	case first = <-nativeEvents:
	}
	if first.err != nil {
		if errors.Is(first.err, io.EOF) {
			return bridgeError(ErrorBrowserUnavailable, "Chrome disconnected before the bridge hello")
		}
		return first.err
	}
	if err := validateHello(first.message); err != nil {
		_ = writeFrame(stdout, errorMessage(first.message.RequestID, err))
		return err
	}

	listener, err := listenEndpoint(runtimeDir)
	if err != nil {
		_ = writeFrame(stdout, errorMessage(first.message.RequestID, err))
		return err
	}
	defer func() { runErr = errors.Join(runErr, listener.Close()) }()

	status := Status{
		Connected:      true,
		ProfileLabel:   first.message.ProfileLabel,
		GrantedOrigins: normalizedOrigins(first.message.GrantedOrigins),
		LastSeenAt:     now().UTC(),
	}
	if err := writeFrame(stdout, wireMessage{ProtocolVersion: protocolVersion, Type: messageResult, RequestID: first.message.RequestID, Status: &status}); err != nil {
		return unavailableFromIO(err)
	}

	hostContext, cancel := context.WithCancel(ctx)
	defer cancel()
	requests := make(chan clientRequest)
	acceptErrors := make(chan error, 1)
	go acceptClients(hostContext, listener, requests, acceptErrors)
	return runBroker(hostContext, stdout, now, status, nativeEvents, requests, acceptErrors)
}

func readNativeMessages(reader io.Reader, events chan<- nativeEvent) {
	for {
		message, err := readFrame(reader)
		events <- nativeEvent{message: message, err: err}
		if err != nil {
			return
		}
	}
}

func acceptClients(ctx context.Context, listener net.Listener, requests chan<- clientRequest, acceptErrors chan<- error) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case acceptErrors <- err:
				return
			}
		}
		go serveClient(ctx, connection, requests)
	}
}

func serveClient(ctx context.Context, connection net.Conn, requests chan<- clientRequest) {
	defer connection.Close()
	message, err := readFrame(connection)
	if err != nil {
		return
	}
	disconnected := make(chan struct{})
	go func() {
		var extra [1]byte
		_, _ = connection.Read(extra[:])
		close(disconnected)
	}()
	reply := make(chan wireMessage, 1)
	select {
	case <-ctx.Done():
		return
	case <-disconnected:
		return
	case requests <- clientRequest{message: message, reply: reply, disconnected: disconnected}:
	}
	select {
	case <-ctx.Done():
		return
	case <-disconnected:
		return
	case response := <-reply:
		_ = writeFrame(connection, response)
	}
}

func runBroker(ctx context.Context, nativeWriter io.Writer, now func() time.Time, status Status, nativeEvents <-chan nativeEvent, requests <-chan clientRequest, acceptErrors <-chan error) error {
	var pending *pendingRequest
	canceled := make(map[string]struct{})
	canceledOrder := make([]string, 0, 16)
	for {
		var availableRequests <-chan clientRequest
		var pendingDisconnected <-chan struct{}
		if pending == nil {
			availableRequests = requests
		} else {
			pendingDisconnected = pending.client.disconnected
		}
		select {
		case <-ctx.Done():
			if pending != nil {
				pending.client.reply <- errorMessage(pending.client.message.RequestID, bridgeError(ErrorBrowserUnavailable, "Chrome browser bridge stopped"))
			}
			return ctx.Err()
		case err := <-acceptErrors:
			return fmt.Errorf("accept browser bridge client: %w", err)
		case <-pendingDisconnected:
			requestID := pending.client.message.RequestID
			canceled[requestID] = struct{}{}
			canceledOrder = append(canceledOrder, requestID)
			if len(canceledOrder) > 128 {
				delete(canceled, canceledOrder[0])
				canceledOrder = canceledOrder[1:]
			}
			pending = nil
		case request := <-availableRequests:
			if _, reused := canceled[request.message.RequestID]; reused {
				request.reply <- errorMessage(request.message.RequestID, bridgeError(ErrorProtocol, "a canceled request_id cannot be reused"))
				continue
			}
			forwarded, response, expected, err := handleClientRequest(request.message, status)
			if err != nil {
				request.reply <- errorMessage(request.message.RequestID, err)
				continue
			}
			if response != nil {
				request.reply <- *response
				continue
			}
			if err := writeFrame(nativeWriter, *forwarded); err != nil {
				request.reply <- errorMessage(request.message.RequestID, bridgeError(ErrorBrowserUnavailable, "Chrome Extension disconnected"))
				return unavailableFromIO(err)
			}
			pending = &pendingRequest{client: request, expected: expected}
		case event := <-nativeEvents:
			if event.err != nil {
				if pending != nil {
					pending.client.reply <- errorMessage(pending.client.message.RequestID, bridgeError(ErrorBrowserUnavailable, "Chrome Extension disconnected"))
				}
				if errors.Is(event.err, io.EOF) {
					return nil
				}
				return event.err
			}
			switch event.message.Type {
			case messagePermissionsChanged:
				if pending != nil && event.message.RequestID == pending.client.message.RequestID {
					return bridgeError(ErrorProtocol, "permission event reused an active request_id")
				}
				if err := validatePermissionsChanged(event.message); err != nil {
					_ = writeFrame(nativeWriter, errorMessage(event.message.RequestID, err))
					continue
				}
				status.GrantedOrigins = normalizedOrigins(event.message.GrantedOrigins)
				status.LastSeenAt = now().UTC()
				if err := writeFrame(nativeWriter, wireMessage{ProtocolVersion: protocolVersion, Type: messageResult, RequestID: event.message.RequestID, Status: statusPointer(status)}); err != nil {
					return unavailableFromIO(err)
				}
			case messageResult, messageError:
				if _, wasCanceled := canceled[event.message.RequestID]; wasCanceled {
					delete(canceled, event.message.RequestID)
					continue
				}
				if pending == nil || event.message.RequestID != pending.client.message.RequestID {
					return bridgeError(ErrorProtocol, "Extension response request_id does not match an active request")
				}
				response, nextStatus, err := handleExtensionResponse(event.message, pending.expected, pending.client.message, status, now().UTC())
				status = nextStatus
				if err != nil {
					pending.client.reply <- errorMessage(pending.client.message.RequestID, err)
				} else {
					pending.client.reply <- response
				}
				pending = nil
			default:
				return bridgeError(ErrorProtocol, "Extension sent a message that is invalid after hello")
			}
		}
	}
}

func handleClientRequest(message wireMessage, status Status) (*wireMessage, *wireMessage, messageType, error) {
	switch message.Type {
	case messageStatus:
		if message.ProfileLabel != "" || len(message.GrantedOrigins) != 0 || message.Status != nil || message.ChannelID != "" || message.PermissionOriginPattern != "" || message.CookieScope != nil || len(message.Cookies) != 0 || hasBrowserFetchFields(message) || message.Error != nil {
			return nil, nil, "", bridgeError(ErrorProtocol, "status request contains unsupported fields")
		}
		response := wireMessage{ProtocolVersion: protocolVersion, Type: messageResult, RequestID: message.RequestID, Status: statusPointer(status)}
		return nil, &response, "", nil
	case messageReadCookies:
		if message.ProfileLabel != "" || len(message.GrantedOrigins) != 0 || message.Status != nil || len(message.Cookies) != 0 || hasBrowserFetchFields(message) || message.Error != nil {
			return nil, nil, "", bridgeError(ErrorProtocol, "read_cookies request contains unsupported fields")
		}
		if message.CookieScope == nil {
			return nil, nil, "", bridgeError(ErrorScopeInvalid, "cookie_scope is required")
		}
		request := ReadCookiesRequest{RequestID: message.RequestID, ChannelID: message.ChannelID, PermissionOriginPattern: message.PermissionOriginPattern, CookieScope: *message.CookieScope}
		if err := validateReadCookiesRequest(request); err != nil {
			return nil, nil, "", err
		}
		if !contains(status.GrantedOrigins, message.PermissionOriginPattern) {
			return nil, nil, "", bridgeError(ErrorBrowserPermissionMissing, "Chrome origin permission is not granted")
		}
		forwarded := message
		return &forwarded, nil, messageReadCookies, nil
	case messageDiscourseSearch:
		if message.ProfileLabel != "" || len(message.GrantedOrigins) != 0 || message.Status != nil || message.CookieScope != nil || len(message.Cookies) != 0 || message.HTTPStatus != 0 || message.Body != "" || message.Error != nil {
			return nil, nil, "", bridgeError(ErrorProtocol, "discourse_search request contains unsupported fields")
		}
		request := DiscourseSearchRequest{RequestID: message.RequestID, ChannelID: message.ChannelID, PermissionOriginPattern: message.PermissionOriginPattern, URL: message.URL}
		if err := validateDiscourseSearchRequest(request); err != nil {
			return nil, nil, "", err
		}
		if !contains(status.GrantedOrigins, message.PermissionOriginPattern) {
			return nil, nil, "", bridgeError(ErrorBrowserPermissionMissing, "Chrome origin permission is not granted")
		}
		forwarded := message
		return &forwarded, nil, messageDiscourseSearch, nil
	case messageRevokePermission:
		if message.ProfileLabel != "" || len(message.GrantedOrigins) != 0 || message.Status != nil || message.ChannelID != "" || message.CookieScope != nil || len(message.Cookies) != 0 || hasBrowserFetchFields(message) || message.Error != nil {
			return nil, nil, "", bridgeError(ErrorProtocol, "revoke_permission request contains unsupported fields")
		}
		if _, err := parsePermissionPattern(message.PermissionOriginPattern); err != nil {
			return nil, nil, "", err
		}
		if !contains(status.GrantedOrigins, message.PermissionOriginPattern) {
			return nil, nil, "", bridgeError(ErrorBrowserPermissionMissing, "Chrome origin permission is not granted")
		}
		forwarded := message
		return &forwarded, nil, messageRevokePermission, nil
	default:
		return nil, nil, "", bridgeError(ErrorProtocol, "local client sent an unsupported message type")
	}
}

func handleExtensionResponse(message wireMessage, expected messageType, request wireMessage, status Status, observedAt time.Time) (wireMessage, Status, error) {
	if message.Type == messageError {
		if message.Error == nil || !allowedExtensionError(message.Error.Code) || message.ProfileLabel != "" || len(message.GrantedOrigins) != 0 || message.Status != nil || message.ChannelID != "" || message.PermissionOriginPattern != "" || message.CookieScope != nil || len(message.Cookies) != 0 || hasBrowserFetchFields(message) {
			return wireMessage{}, status, bridgeError(ErrorProtocol, "Extension returned an invalid error")
		}
		status.LastSeenAt = observedAt
		return wireMessage{}, status, safeExtensionError(message.Error.Code)
	}
	switch expected {
	case messageReadCookies:
		if message.ProfileLabel != "" || len(message.GrantedOrigins) != 0 || message.Status != nil || message.ChannelID != "" || message.PermissionOriginPattern != "" || message.CookieScope != nil || hasBrowserFetchFields(message) || message.Error != nil {
			return wireMessage{}, status, bridgeError(ErrorProtocol, "Extension returned invalid read_cookies fields")
		}
		readRequest := ReadCookiesRequest{RequestID: request.RequestID, ChannelID: request.ChannelID, PermissionOriginPattern: request.PermissionOriginPattern, CookieScope: *request.CookieScope}
		if err := ValidateCookies(readRequest, message.Cookies); err != nil {
			return wireMessage{}, status, err
		}
		status.LastSeenAt = observedAt
		return wireMessage{ProtocolVersion: protocolVersion, Type: messageResult, RequestID: request.RequestID, Cookies: append([]Cookie(nil), message.Cookies...)}, status, nil
	case messageDiscourseSearch:
		if message.ProfileLabel != "" || len(message.GrantedOrigins) != 0 || message.Status != nil || message.ChannelID != "" || message.PermissionOriginPattern != "" || message.CookieScope != nil || len(message.Cookies) != 0 || message.URL != "" || message.Error != nil {
			return wireMessage{}, status, bridgeError(ErrorProtocol, "Extension returned invalid discourse_search fields")
		}
		response := DiscourseSearchResponse{RequestID: request.RequestID, HTTPStatus: message.HTTPStatus, Body: message.Body}
		if err := validateDiscourseSearchResponse(response); err != nil {
			return wireMessage{}, status, err
		}
		status.LastSeenAt = observedAt
		return wireMessage{ProtocolVersion: protocolVersion, Type: messageResult, RequestID: request.RequestID, HTTPStatus: message.HTTPStatus, Body: message.Body}, status, nil
	case messageRevokePermission:
		if message.ProfileLabel != "" || message.Status != nil || message.ChannelID != "" || message.PermissionOriginPattern != "" || message.CookieScope != nil || len(message.Cookies) != 0 || hasBrowserFetchFields(message) || message.Error != nil {
			return wireMessage{}, status, bridgeError(ErrorProtocol, "Extension returned invalid revoke_permission fields")
		}
		if err := validateGrantedOrigins(message.GrantedOrigins); err != nil {
			return wireMessage{}, status, err
		}
		if contains(message.GrantedOrigins, request.PermissionOriginPattern) {
			return wireMessage{}, status, bridgeError(ErrorBrowserPermissionMissing, "Chrome did not revoke the requested origin permission")
		}
		status.GrantedOrigins = normalizedOrigins(message.GrantedOrigins)
		status.LastSeenAt = observedAt
		return wireMessage{ProtocolVersion: protocolVersion, Type: messageResult, RequestID: request.RequestID, Status: statusPointer(status)}, status, nil
	default:
		return wireMessage{}, status, bridgeError(ErrorProtocol, "Extension response has no matching operation")
	}
}

func validateHello(message wireMessage) error {
	if message.Type != messageHello || strings.TrimSpace(message.ProfileLabel) == "" || message.ProfileLabel != strings.TrimSpace(message.ProfileLabel) || len(message.ProfileLabel) > 128 || strings.ContainsFunc(message.ProfileLabel, unicode.IsControl) {
		return bridgeError(ErrorProtocol, "first native message must be a profile hello")
	}
	if message.Status != nil || message.ChannelID != "" || message.PermissionOriginPattern != "" || message.CookieScope != nil || len(message.Cookies) != 0 || hasBrowserFetchFields(message) || message.Error != nil {
		return bridgeError(ErrorProtocol, "profile hello contains unsupported fields")
	}
	return validateGrantedOrigins(message.GrantedOrigins)
}

func validatePermissionsChanged(message wireMessage) error {
	if message.Type != messagePermissionsChanged {
		return bridgeError(ErrorProtocol, "invalid permission event")
	}
	if message.ProfileLabel != "" || message.Status != nil || message.ChannelID != "" || message.PermissionOriginPattern != "" || message.CookieScope != nil || len(message.Cookies) != 0 || hasBrowserFetchFields(message) || message.Error != nil {
		return bridgeError(ErrorProtocol, "permission event contains unsupported fields")
	}
	return validateGrantedOrigins(message.GrantedOrigins)
}

func statusPointer(status Status) *Status {
	copy := copyStatus(status)
	return &copy
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func allowedExtensionError(code ErrorCode) bool {
	switch code {
	case ErrorBrowserPermissionMissing, ErrorCookieMissing, ErrorScopeInvalid, ErrorBrowserRequestFailed:
		return true
	default:
		return false
	}
}

func safeExtensionError(code ErrorCode) *BridgeError {
	switch code {
	case ErrorBrowserPermissionMissing:
		return bridgeError(code, "Chrome origin permission is not granted")
	case ErrorCookieMissing:
		return bridgeError(code, "required cookies are absent")
	case ErrorScopeInvalid:
		return bridgeError(code, "Chrome Extension rejected the authorized cookie scope")
	case ErrorBrowserRequestFailed:
		return bridgeError(code, "Chrome could not complete the authorized request")
	default:
		return bridgeError(ErrorProtocol, "Chrome Extension returned an unsupported error")
	}
}

func hasBrowserFetchFields(message wireMessage) bool {
	return message.URL != "" || message.HTTPStatus != 0 || message.Body != ""
}
