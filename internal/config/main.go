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
	DefaultConfigDir      = "conf"
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
	// UnixSocketPath 為可選的 Unix domain socket 路徑；留空表示不啟用 UDS 監聽。
	// 可與 listen_address 並存，但兩者至少需設定一個。
	UnixSocketPath string          `toml:"unix_socket_path"`
	Keepalive      KeepaliveConfig `toml:"keepalive"`
}

// KeepaliveConfig 為 gRPC server 的 keepalive 時間設定。
type KeepaliveConfig struct {
	// Time 為連線閒置多久後主動送 keepalive ping 探活。
	Time Duration `toml:"time"`
	// Timeout 為送出 ping 後等待回應的逾時，逾時未收到回應即斷線。
	Timeout Duration `toml:"timeout"`
	// MinTime 為允許 client 端 keepalive ping 的最短間隔，過於頻繁會被視為違規而中斷連線。
	MinTime Duration `toml:"min_time"`
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
	listenAddr := strings.TrimSpace(cfg.Server.ListenAddress)
	socketPath := strings.TrimSpace(cfg.Server.UnixSocketPath)
	if listenAddr == "" && socketPath == "" {
		return fmt.Errorf("server: either listen_address or unix_socket_path must be set")
	}
	if listenAddr != "" {
		if _, _, err := net.SplitHostPort(listenAddr); err != nil {
			return fmt.Errorf("server.listen_address %q: %w", cfg.Server.ListenAddress, err)
		}
	}
	if socketPath != "" {
		if !filepath.IsAbs(socketPath) {
			return fmt.Errorf("server.unix_socket_path %q must be an absolute path", socketPath)
		}
		// Linux sun_path 上限為 108 bytes（含結尾 NUL），路徑過長 bind 會失敗。
		if len(socketPath) > 107 {
			return fmt.Errorf("server.unix_socket_path %q too long (%d bytes, max 107)", socketPath, len(socketPath))
		}
		if strings.ContainsRune(socketPath, 0) {
			return fmt.Errorf("server.unix_socket_path must not contain NUL byte")
		}
	}

	if cfg.Server.Keepalive.Time.Std() <= 0 {
		return fmt.Errorf("server.keepalive.time must be > 0")
	}
	if cfg.Server.Keepalive.Timeout.Std() <= 0 {
		return fmt.Errorf("server.keepalive.timeout must be > 0")
	}
	if cfg.Server.Keepalive.MinTime.Std() <= 0 {
		return fmt.Errorf("server.keepalive.min_time must be > 0")
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

// DefaultConfigPath 回傳 exe 同目錄底下的 conf/authd.toml 絕對路徑。
func DefaultConfigPath() string {
	exe, err := os.Executable()
	if err != nil {
		return filepath.Join(DefaultConfigDir, DefaultConfigFileName)
	}
	return filepath.Join(filepath.Dir(exe), DefaultConfigDir, DefaultConfigFileName)
}
