//go:build !windows

package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func installHostManifest(payload []byte) (InstallResult, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return InstallResult{}, fmt.Errorf("resolve Chrome configuration directory: %w", err)
	}
	var chromeDir string
	if runtime.GOOS == "darwin" {
		chromeDir = filepath.Join(configDir, "Google", "Chrome", "NativeMessagingHosts")
	} else {
		chromeDir = filepath.Join(configDir, "google-chrome", "NativeMessagingHosts")
	}
	if err := os.MkdirAll(chromeDir, 0o700); err != nil {
		return InstallResult{}, fmt.Errorf("create Chrome native host directory: %w", err)
	}
	manifestPath := filepath.Join(chromeDir, HostName+".json")
	if err := os.WriteFile(manifestPath, payload, 0o600); err != nil {
		return InstallResult{}, fmt.Errorf("write Chrome native host manifest: %w", err)
	}
	if err := os.Chmod(manifestPath, 0o600); err != nil {
		return InstallResult{}, fmt.Errorf("protect Chrome native host manifest: %w", err)
	}
	return InstallResult{ManifestPath: manifestPath}, nil
}
