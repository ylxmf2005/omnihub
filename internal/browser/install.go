package browser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type nativeHostManifest struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Path           string   `json:"path"`
	Type           string   `json:"type"`
	AllowedOrigins []string `json:"allowed_origins"`
}

// UninstallResult 只描述 Native Host 注册是否被移除，不回显 manifest 内容或任何凭据。
type UninstallResult struct {
	Registration string `json:"registration"`
	Removed      bool   `json:"removed"`
}

func InstallHost(executable, extensionID string) (InstallResult, error) {
	if !validExtensionID(extensionID) {
		return InstallResult{}, bridgeError(ErrorScopeInvalid, "Chrome extension ID must be exactly 32 characters in the range a-p")
	}
	absolute, err := filepath.Abs(executable)
	if err != nil {
		return InstallResult{}, fmt.Errorf("resolve native host executable: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return InstallResult{}, fmt.Errorf("inspect native host executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return InstallResult{}, bridgeError(ErrorScopeInvalid, "native host executable must be a regular file")
	}
	manifest := nativeHostManifest{
		Name:           HostName,
		Description:    "OmniHub Chrome browser bridge",
		Path:           absolute,
		Type:           "stdio",
		AllowedOrigins: []string{"chrome-extension://" + extensionID + "/"},
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return InstallResult{}, fmt.Errorf("encode native host manifest: %w", err)
	}
	payload = append(payload, '\n')
	return installHostManifest(payload)
}

func validExtensionID(extensionID string) bool {
	if len(extensionID) != 32 {
		return false
	}
	for _, character := range extensionID {
		if character < 'a' || character > 'p' {
			return false
		}
	}
	return true
}
