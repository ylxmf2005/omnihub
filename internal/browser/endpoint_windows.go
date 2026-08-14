//go:build windows

package browser

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"path/filepath"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

type endpointListener struct {
	net.Listener
}

func listenEndpoint(runtimeDir string) (*endpointListener, error) {
	if runtimeDir == "" {
		return nil, bridgeError(ErrorScopeInvalid, "runtime directory is required")
	}
	pipeName, sid, err := windowsEndpoint(runtimeDir)
	if err != nil {
		return nil, err
	}
	securityDescriptor := "D:P(A;;GA;;;" + sid + ")"
	if _, err := winio.SddlToSecurityDescriptor(securityDescriptor); err != nil {
		return nil, fmt.Errorf("build current-user browser bridge ACL: %w", err)
	}
	listener, err := winio.ListenPipe(pipeName, &winio.PipeConfig{
		SecurityDescriptor: securityDescriptor,
		InputBufferSize:    maxMessageSize + 4,
		OutputBufferSize:   maxMessageSize + 4,
	})
	if err != nil {
		// go-winio 以 FILE_CREATE 建 first pipe instance；这些 DOS errno 才能
		// 证明名称已存在、pipe 忙或现有 endpoint 拒绝当前用户访问。
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_PIPE_BUSY) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil, bridgeError(ErrorBridgeAlreadyActive, "another Chrome profile bridge is already active")
		}
		return nil, fmt.Errorf("listen on current-user browser bridge named pipe: %w", err)
	}
	return &endpointListener{Listener: listener}, nil
}

func dialEndpoint(ctx context.Context, runtimeDir string) (net.Conn, error) {
	if runtimeDir == "" {
		return nil, bridgeError(ErrorBrowserUnavailable, "browser bridge runtime directory is not configured")
	}
	pipeName, _, err := windowsEndpoint(runtimeDir)
	if err != nil {
		return nil, bridgeError(ErrorBrowserUnavailable, "current Windows user SID is unavailable")
	}
	connection, err := winio.DialPipeContext(ctx, pipeName)
	if err != nil {
		return nil, bridgeError(ErrorBrowserUnavailable, "Chrome browser bridge is offline")
	}
	return connection, nil
}

func windowsEndpoint(runtimeDir string) (string, string, error) {
	absolute, err := filepath.Abs(runtimeDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve browser runtime directory: %w", err)
	}
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", "", fmt.Errorf("resolve current Windows user: %w", err)
	}
	sid := tokenUser.User.Sid.String()
	digest := sha256.Sum256([]byte(sid + "\x00" + filepath.Clean(absolute)))
	return fmt.Sprintf(`\\.\pipe\omnihub-chrome-%x`, digest[:12]), sid, nil
}
