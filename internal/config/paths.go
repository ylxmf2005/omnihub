package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const applicationName = "omnihub"

// Paths 把配置、状态、缓存和 runtime IPC 分开，避免后续把 SQLite、Cookie Bridge socket 与可移植配置混放。
type Paths struct {
	ConfigDir  string `json:"config_dir"`
	StateDir   string `json:"state_dir"`
	CacheDir   string `json:"cache_dir"`
	RuntimeDir string `json:"runtime_dir"`
	Database   string `json:"database"`
}

func Resolve() (Paths, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve user config directory: %w", err)
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve user cache directory: %w", err)
	}
	stateDir, runtimeDir, err := platformStateAndRuntime()
	if err != nil {
		return Paths{}, err
	}

	paths := Paths{
		ConfigDir:  filepath.Join(configDir, applicationName),
		StateDir:   filepath.Join(stateDir, applicationName),
		CacheDir:   filepath.Join(cacheDir, applicationName),
		RuntimeDir: filepath.Join(runtimeDir, applicationName),
	}
	paths.Database = filepath.Join(paths.StateDir, "omnihub.db")
	return paths, nil
}

func (paths Paths) Ensure() error {
	for _, directory := range []string{paths.ConfigDir, paths.StateDir, paths.CacheDir, paths.RuntimeDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", directory, err)
		}
		if runtime.GOOS != "windows" {
			if err := os.Chmod(directory, 0o700); err != nil {
				return fmt.Errorf("protect %s: %w", directory, err)
			}
		}
	}
	return nil
}

func platformStateAndRuntime() (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve home directory: %w", err)
	}
	switch runtime.GOOS {
	case "darwin":
		base := filepath.Join(home, "Library", "Application Support")
		return base, filepath.Join(home, "Library", "Caches"), nil
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Local")
		}
		return base, base, nil
	default:
		state := os.Getenv("XDG_STATE_HOME")
		if state == "" {
			state = filepath.Join(home, ".local", "state")
		}
		runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
		if runtimeDir == "" {
			runtimeDir = filepath.Join(state, "run")
		}
		return state, runtimeDir, nil
	}
}
