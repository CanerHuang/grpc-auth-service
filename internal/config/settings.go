package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

const DefaultSettingsFileName = "authd-settings.toml"

// Settings 是執行期可變的設定，由 gRPC 介面直接寫入，並會持久化到磁碟。
type Settings struct {
	RefreshTokenExtendOnRefresh bool `toml:"refresh_token_extend_on_refresh"`
}

func defaultSettings() Settings {
	return Settings{
		RefreshTokenExtendOnRefresh: true,
	}
}

// SettingsStore 提供 thread-safe 的 Get/Set，並在 Set 時將設定寫回檔案。
type SettingsStore struct {
	mu       sync.RWMutex
	path     string
	settings Settings
}

// DefaultSettingsPath 回傳 exe 同目錄底下的 config/authd-settings.toml 絕對路徑。
func DefaultSettingsPath() string {
	exe, err := os.Executable()
	if err != nil {
		return filepath.Join(DefaultConfigDir, DefaultSettingsFileName)
	}
	return filepath.Join(filepath.Dir(exe), DefaultConfigDir, DefaultSettingsFileName)
}

// LoadSettings 載入 authd-settings.toml。若檔案不存在，會以預設值建立並寫盤。
func LoadSettings(path string) (*SettingsStore, error) {
	settingsPath := strings.TrimSpace(path)
	if settingsPath == "" {
		settingsPath = DefaultSettingsPath()
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read authd settings %q failed: %w", settingsPath, err)
		}
		s := defaultSettings()
		if err := writeSettings(settingsPath, s); err != nil {
			return nil, err
		}
		return &SettingsStore{path: settingsPath, settings: s}, nil
	}

	s := defaultSettings()
	if err := toml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse auth settings %q failed: %w", settingsPath, err)
	}
	return &SettingsStore{path: settingsPath, settings: s}, nil
}

func (s *SettingsStore) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

func (s *SettingsStore) RefreshTokenExtendOnRefresh() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings.RefreshTokenExtendOnRefresh
}

// SetRefreshTokenExtendOnRefresh 更新欄位並立即將整份 settings 寫回磁碟。
func (s *SettingsStore) SetRefreshTokenExtendOnRefresh(v bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	updated := s.settings
	updated.RefreshTokenExtendOnRefresh = v
	if err := writeSettings(s.path, updated); err != nil {
		return err
	}
	s.settings = updated
	return nil
}

func writeSettings(path string, s Settings) error {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create auth settings dir failed: %w", err)
		}
	}
	data, err := toml.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal auth settings failed: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write auth settings %q failed: %w", path, err)
	}
	return nil
}
