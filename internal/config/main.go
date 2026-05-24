package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const (
	DefaultConfigDir      = "config"
	DefaultConfigFileName = "authd.toml"
)

type Config struct {
	Server  ServerConfig  `toml:"server"`
	Storage StorageConfig `toml:"storage"`
	Auth    AuthConfig    `toml:"auth"`
	DefaultSetting Settings `toml:"default_setting"`
}

type ServerConfig struct {
	ListenAddress string `toml:"listen_address"`
}

type StorageConfig struct {
	SQLitePath  string   `toml:"sqlite_path"`
	BusyTimeout Duration `toml:"busy_timeout"`
}

type AuthConfig struct {
	Issuer                    string   `toml:"issuer"`
	AccessTokenTTL            Duration `toml:"access_token_ttl"`
	RefreshTokenTTL           Duration `toml:"refresh_token_ttl"`
	MaxUsers                  uint32   `toml:"max_users"`
	SigningKey                string   `toml:"signing_key"`
	BootstrapAdminUsername    string   `toml:"bootstrap_admin_username"`
	BootstrapAdminPassword    string   `toml:"bootstrap_admin_password"`
	BootstrapAdminDisplayName string   `toml:"bootstrap_admin_display_name"`
	BootstrapAdminRoles       []string `toml:"bootstrap_admin_roles"`
}

// Load 嚴格載入 authd.toml：檔案必須存在，所有欄位必填且需通過合理性檢查。
// 任何缺失或不合理的值都會回傳 error，不再自動補預設值。
func Load(path string) (Config, error) {
	configPath := strings.TrimSpace(path)
	if configPath == "" {
		configPath = DefaultConfigPath()
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("read auth config %q failed: %w", configPath, err)
	}

	var cfg Config
	dec := toml.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse auth config %q failed: %w", configPath, err)
	}

	if err := validate(&cfg); err != nil {
		return Config{}, fmt.Errorf("invalid auth config %q: %w", configPath, err)
	}
	return cfg, nil
}

func validate(cfg *Config) error {
	if _, _, err := net.SplitHostPort(strings.TrimSpace(cfg.Server.ListenAddress)); err != nil {
		return fmt.Errorf("server.listen_address %q: %w", cfg.Server.ListenAddress, err)
	}

	if strings.TrimSpace(cfg.Storage.SQLitePath) == "" {
		return fmt.Errorf("storage.sqlite_path must not be empty")
	}
	if cfg.Storage.BusyTimeout.Std() <= 0 {
		return fmt.Errorf("storage.busy_timeout must be > 0")
	}

	if strings.TrimSpace(cfg.Auth.Issuer) == "" {
		return fmt.Errorf("auth.issuer must not be empty")
	}
	if cfg.Auth.AccessTokenTTL.Std() < time.Second {
		return fmt.Errorf("auth.access_token_ttl must be >= 1s, got %s", cfg.Auth.AccessTokenTTL.Std())
	}
	if cfg.Auth.RefreshTokenTTL.Std() < time.Second {
		return fmt.Errorf("auth.refresh_token_ttl must be >= 1s, got %s", cfg.Auth.RefreshTokenTTL.Std())
	}
	if cfg.Auth.RefreshTokenTTL.Std() < cfg.Auth.AccessTokenTTL.Std() {
		return fmt.Errorf("auth.refresh_token_ttl (%s) must be >= access_token_ttl (%s)",
			cfg.Auth.RefreshTokenTTL.Std(), cfg.Auth.AccessTokenTTL.Std())
	}
	if cfg.Auth.MaxUsers == 0 {
		return fmt.Errorf("auth.max_users must be > 0")
	}
	if strings.TrimSpace(cfg.Auth.SigningKey) == "" {
		return fmt.Errorf("auth.signing_key must not be empty")
	}
	if strings.TrimSpace(cfg.Auth.BootstrapAdminUsername) == "" {
		return fmt.Errorf("auth.bootstrap_admin_username must not be empty")
	}
	if strings.TrimSpace(cfg.Auth.BootstrapAdminPassword) == "" {
		return fmt.Errorf("auth.bootstrap_admin_password must not be empty")
	}
	if strings.TrimSpace(cfg.Auth.BootstrapAdminDisplayName) == "" {
		return fmt.Errorf("auth.bootstrap_admin_display_name must not be empty")
	}
	if len(cfg.Auth.BootstrapAdminRoles) == 0 {
		return fmt.Errorf("auth.bootstrap_admin_roles must not be empty")
	}
	for i, role := range cfg.Auth.BootstrapAdminRoles {
		if strings.TrimSpace(role) == "" {
			return fmt.Errorf("auth.bootstrap_admin_roles[%d] must not be empty", i)
		}
	}
	return nil
}

// DefaultConfigPath 回傳 exe 同目錄底下的 config/authd.toml 絕對路徑。
func DefaultConfigPath() string {
	exe, err := os.Executable()
	if err != nil {
		return filepath.Join(DefaultConfigDir, DefaultConfigFileName)
	}
	return filepath.Join(filepath.Dir(exe), DefaultConfigDir, DefaultConfigFileName)
}
