//go:build !windows

package browser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const socketFileName = "chrome.sock"

type endpointListener struct {
	net.Listener
	path     string
	lockFile *os.File
}

func listenEndpoint(runtimeDir string) (*endpointListener, error) {
	if runtimeDir == "" {
		return nil, bridgeError(ErrorScopeInvalid, "runtime directory is required")
	}
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return nil, fmt.Errorf("create browser runtime directory: %w", err)
	}
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		return nil, fmt.Errorf("protect browser runtime directory: %w", err)
	}

	// lock 只负责单活；socket 自身仍做 stale 探测，崩溃后的残留不会阻断 Chrome 重连。
	lockFile, err := os.OpenFile(filepath.Join(runtimeDir, "chrome.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open browser bridge lock: %w", err)
	}
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lockFile.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, bridgeError(ErrorBridgeAlreadyActive, "another Chrome profile bridge is already active")
		}
		return nil, fmt.Errorf("lock browser bridge endpoint: %w", err)
	}

	path := filepath.Join(runtimeDir, socketFileName)
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSocket == 0 {
			_ = unlockFile(lockFile)
			return nil, bridgeError(ErrorScopeInvalid, "browser bridge endpoint is not a Unix socket")
		}
		connection, dialErr := net.DialTimeout("unix", path, 50*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			_ = unlockFile(lockFile)
			return nil, bridgeError(ErrorBridgeAlreadyActive, "browser bridge socket is already accepting connections")
		}
		if removeErr := os.Remove(path); removeErr != nil {
			_ = unlockFile(lockFile)
			return nil, fmt.Errorf("remove stale browser bridge socket: %w", removeErr)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		_ = unlockFile(lockFile)
		return nil, fmt.Errorf("inspect browser bridge socket: %w", statErr)
	}

	listener, err := net.Listen("unix", path)
	if err != nil {
		_ = unlockFile(lockFile)
		return nil, fmt.Errorf("listen on browser bridge socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		_ = unlockFile(lockFile)
		return nil, fmt.Errorf("protect browser bridge socket: %w", err)
	}
	return &endpointListener{Listener: listener, path: path, lockFile: lockFile}, nil
}

func (listener *endpointListener) Close() error {
	listenErr := listener.Listener.Close()
	removeErr := os.Remove(listener.path)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	lockErr := unlockFile(listener.lockFile)
	return errors.Join(listenErr, removeErr, lockErr)
}

func dialEndpoint(ctx context.Context, runtimeDir string) (net.Conn, error) {
	if runtimeDir == "" {
		return nil, bridgeError(ErrorBrowserUnavailable, "browser bridge runtime directory is not configured")
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(runtimeDir, socketFileName))
	if err != nil {
		return nil, bridgeError(ErrorBrowserUnavailable, "Chrome browser bridge is offline")
	}
	return connection, nil
}

func unlockFile(file *os.File) error {
	if file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
	return errors.Join(unlockErr, file.Close())
}
