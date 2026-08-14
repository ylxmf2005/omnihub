//go:build !windows

package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func installHostManifest(payload []byte) (InstallResult, error) {
	manifestPath, err := chromeNativeHostManifestPath()
	if err != nil {
		return InstallResult{}, err
	}
	chromeDir := filepath.Dir(manifestPath)
	if err := os.MkdirAll(chromeDir, 0o700); err != nil {
		return InstallResult{}, fmt.Errorf("create Chrome native host directory: %w", err)
	}
	if err := os.WriteFile(manifestPath, payload, 0o600); err != nil {
		return InstallResult{}, fmt.Errorf("write Chrome native host manifest: %w", err)
	}
	if err := os.Chmod(manifestPath, 0o600); err != nil {
		return InstallResult{}, fmt.Errorf("protect Chrome native host manifest: %w", err)
	}
	return InstallResult{ManifestPath: manifestPath}, nil
}

// UninstallHost 只删除当前用户下 HostName 对应的精确 manifest；目录、binary 与用户数据均保留。
func UninstallHost() (UninstallResult, error) {
	manifestPath, err := chromeNativeHostManifestPath()
	if err != nil {
		return UninstallResult{}, err
	}
	if err := os.Remove(manifestPath); err != nil {
		if os.IsNotExist(err) {
			return UninstallResult{Registration: manifestPath}, nil
		}
		return UninstallResult{}, fmt.Errorf("remove Chrome native host manifest: %w", err)
	}
	return UninstallResult{Registration: manifestPath, Removed: true}, nil
}

func chromeNativeHostManifestPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve Chrome configuration directory: %w", err)
	}
	var chromeDir string
	if runtime.GOOS == "darwin" {
		chromeDir = filepath.Join(configDir, "Google", "Chrome", "NativeMessagingHosts")
	} else {
		chromeDir = filepath.Join(configDir, "google-chrome", "NativeMessagingHosts")
	}
	return filepath.Join(chromeDir, HostName+".json"), nil
}
