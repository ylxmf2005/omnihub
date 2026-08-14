//go:build windows

package browser

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

const chromeNativeHostRegistryKey = `Software\Google\Chrome\NativeMessagingHosts\` + HostName

func installHostManifest(payload []byte) (InstallResult, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return InstallResult{}, fmt.Errorf("resolve Chrome configuration directory: %w", err)
		}
		base = configDir
	}
	manifestDir := filepath.Join(base, "OmniHub", "NativeMessagingHosts")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		return InstallResult{}, fmt.Errorf("create Chrome native host directory: %w", err)
	}
	manifestPath := filepath.Join(manifestDir, HostName+".json")
	if err := os.WriteFile(manifestPath, payload, 0o600); err != nil {
		return InstallResult{}, fmt.Errorf("write Chrome native host manifest: %w", err)
	}

	key, _, err := registry.CreateKey(registry.CURRENT_USER, chromeNativeHostRegistryKey, registry.SET_VALUE)
	if err != nil {
		return InstallResult{}, fmt.Errorf("register Chrome native host: %w", err)
	}
	defer key.Close()
	if err := key.SetStringValue("", manifestPath); err != nil {
		return InstallResult{}, fmt.Errorf("write Chrome native host registry value: %w", err)
	}
	return InstallResult{ManifestPath: manifestPath, RegistryKey: `HKCU\` + chromeNativeHostRegistryKey}, nil
}

// UninstallHost 只删除当前用户下 HostName 的精确注册键；manifest 文件、binary 与用户数据均保留。
func UninstallHost() (UninstallResult, error) {
	registration := `HKCU\` + chromeNativeHostRegistryKey
	if err := registry.DeleteKey(registry.CURRENT_USER, chromeNativeHostRegistryKey); err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return UninstallResult{Registration: registration}, nil
		}
		return UninstallResult{}, fmt.Errorf("remove Chrome native host registry key: %w", err)
	}
	return UninstallResult{Registration: registration, Removed: true}, nil
}
