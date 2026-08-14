package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"time"
)

// Client 对当前用户的 Browser Bridge 串行发出短请求；每次重新拨号使 Chrome 重连后无需重建 Client。
type Client struct {
	runtimeDir  string
	requestSlot chan struct{}
}

func NewClient(runtimeDir string) *Client {
	return &Client{runtimeDir: runtimeDir, requestSlot: make(chan struct{}, 1)}
}

func (client *Client) Status(ctx context.Context) (Status, error) {
	requestID, err := newRequestID()
	if err != nil {
		return Status{}, err
	}
	response, err := client.exchange(ctx, wireMessage{ProtocolVersion: protocolVersion, Type: messageStatus, RequestID: requestID})
	if err != nil {
		return Status{}, err
	}
	if response.Status == nil || !response.Status.Connected {
		return Status{}, bridgeError(ErrorProtocol, "browser bridge returned an invalid status result")
	}
	return copyStatus(*response.Status), nil
}

func (client *Client) ReadCookies(ctx context.Context, request ReadCookiesRequest) (ReadCookiesResponse, error) {
	if err := validateReadCookiesRequest(request); err != nil {
		return ReadCookiesResponse{}, err
	}
	scope := request.CookieScope
	response, err := client.exchange(ctx, wireMessage{
		ProtocolVersion:         protocolVersion,
		Type:                    messageReadCookies,
		RequestID:               request.RequestID,
		ChannelID:               request.ChannelID,
		PermissionOriginPattern: request.PermissionOriginPattern,
		CookieScope:             &scope,
	})
	if err != nil {
		return ReadCookiesResponse{}, err
	}
	if err := ValidateCookies(request, response.Cookies); err != nil {
		return ReadCookiesResponse{}, err
	}
	return ReadCookiesResponse{RequestID: response.RequestID, Cookies: append([]Cookie(nil), response.Cookies...)}, nil
}

func (client *Client) RevokePermission(ctx context.Context, permissionOriginPattern string) (RevokePermissionResponse, error) {
	if _, err := parsePermissionPattern(permissionOriginPattern); err != nil {
		return RevokePermissionResponse{}, err
	}
	requestID, err := newRequestID()
	if err != nil {
		return RevokePermissionResponse{}, err
	}
	response, err := client.exchange(ctx, wireMessage{
		ProtocolVersion:         protocolVersion,
		Type:                    messageRevokePermission,
		RequestID:               requestID,
		PermissionOriginPattern: permissionOriginPattern,
	})
	if err != nil {
		return RevokePermissionResponse{}, err
	}
	if response.Status == nil || !response.Status.Connected {
		return RevokePermissionResponse{}, bridgeError(ErrorProtocol, "browser bridge returned an invalid revoke result")
	}
	return RevokePermissionResponse{RequestID: response.RequestID, Status: copyStatus(*response.Status)}, nil
}

func (client *Client) exchange(ctx context.Context, request wireMessage) (wireMessage, error) {
	select {
	case client.requestSlot <- struct{}{}:
		defer func() { <-client.requestSlot }()
	case <-ctx.Done():
		return wireMessage{}, ctx.Err()
	}

	connection, err := dialEndpoint(ctx, client.runtimeDir)
	if err != nil {
		return wireMessage{}, err
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { _ = connection.SetDeadline(time.Now()) })
	defer stop()

	if err := writeFrame(connection, request); err != nil {
		if ctx.Err() != nil {
			return wireMessage{}, ctx.Err()
		}
		return wireMessage{}, unavailableFromIO(err)
	}
	response, err := readFrame(connection)
	if err != nil {
		if ctx.Err() != nil {
			return wireMessage{}, ctx.Err()
		}
		return wireMessage{}, unavailableFromIO(err)
	}
	if response.RequestID != request.RequestID {
		return wireMessage{}, bridgeError(ErrorProtocol, "browser bridge response request_id does not match")
	}
	if ctx.Err() != nil {
		return wireMessage{}, ctx.Err()
	}
	switch response.Type {
	case messageResult:
		return response, nil
	case messageError:
		if response.Error == nil || response.Error.Code == "" {
			return wireMessage{}, bridgeError(ErrorProtocol, "browser bridge returned an invalid error")
		}
		return wireMessage{}, &BridgeError{Code: response.Error.Code, Message: response.Error.Message}
	default:
		return wireMessage{}, bridgeError(ErrorProtocol, "browser bridge returned an unexpected message type")
	}
}

func unavailableFromIO(err error) error {
	var typed *BridgeError
	if errors.As(err, &typed) {
		return err
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return bridgeError(ErrorBrowserUnavailable, "Chrome browser bridge disconnected")
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return bridgeError(ErrorBrowserUnavailable, "Chrome browser bridge is unavailable")
	}
	return bridgeError(ErrorBrowserUnavailable, "Chrome browser bridge is unavailable")
}

func newRequestID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "browserreq_" + hex.EncodeToString(random[:]), nil
}

func copyStatus(status Status) Status {
	status.GrantedOrigins = append([]string{}, status.GrantedOrigins...)
	return status
}
