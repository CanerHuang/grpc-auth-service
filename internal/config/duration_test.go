package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"authd/internal/config"

	"github.com/pelletier/go-toml/v2"
)

func TestDuration_UnmarshalText_CommonFormats(t *testing.T) {
	cases := []struct {
		input string
		want  time.Duration
	}{
		{"30s", 30 * time.Second},
		{"1m", time.Minute},
		{"5m", 5 * time.Minute},
		{"1h", time.Hour},
		{"1h30m", time.Hour + 30*time.Minute},
		{"2h45m30s", 2*time.Hour + 45*time.Minute + 30*time.Second},
		{"500ms", 500 * time.Millisecond},
		{"100us", 100 * time.Microsecond},
		{"1ns", time.Nanosecond},
		{"  5m  ", 5 * time.Minute}, // trims whitespace
		{"0s", 0},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			var d config.Duration
			if err := d.UnmarshalText([]byte(tc.input)); err != nil {
				t.Fatalf("unmarshal %q: %v", tc.input, err)
			}
			if d.Std() != tc.want {
				t.Errorf("got %v, want %v", d.Std(), tc.want)
			}
		})
	}
}

func TestDuration_UnmarshalText_Invalid(t *testing.T) {
	cases := []string{
		"",     // empty
		"   ",  // whitespace only
		"abc",  // garbage
		"5",    // no unit
		"1d",   // days not supported by time.ParseDuration
		"-",    // dangling sign
		"1m30", // missing trailing unit
		"5 m",  // internal whitespace
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			var d config.Duration
			if err := d.UnmarshalText([]byte(in)); err == nil {
				t.Errorf("expected error for %q, got nil (parsed as %v)", in, d.Std())
			}
		})
	}
}

func TestDuration_MarshalText(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{time.Minute, "1m0s"},
		{5 * time.Minute, "5m0s"},
		{time.Hour, "1h0m0s"},
		{time.Hour + 30*time.Minute, "1h30m0s"},
		{500 * time.Millisecond, "500ms"},
		{0, "0s"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			text, err := config.Duration(tc.d).MarshalText()
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(text) != tc.want {
				t.Errorf("got %q, want %q", text, tc.want)
			}
		})
	}
}

func TestDuration_RoundTrip(t *testing.T) {
	cases := []time.Duration{
		30 * time.Second,
		time.Minute,
		5 * time.Minute,
		time.Hour,
		time.Hour + 30*time.Minute,
		2*time.Hour + 45*time.Minute + 30*time.Second,
		500 * time.Millisecond,
		0,
	}
	for _, d := range cases {
		t.Run(d.String(), func(t *testing.T) {
			orig := config.Duration(d)
			text, err := orig.MarshalText()
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got config.Duration
			if err := got.UnmarshalText(text); err != nil {
				t.Fatalf("unmarshal %q: %v", text, err)
			}
			if got != orig {
				t.Errorf("round trip: got %v, want %v (text=%q)", got.Std(), orig.Std(), text)
			}
		})
	}
}

func TestDuration_TOMLRoundTrip(t *testing.T) {
	type doc struct {
		AccessTokenTTL  config.Duration `toml:"access_token_ttl"`
		RefreshTokenTTL config.Duration `toml:"refresh_token_ttl"`
		BusyTimeout     config.Duration `toml:"busy_timeout"`
	}

	original := doc{
		AccessTokenTTL:  config.Duration(5 * time.Minute),
		RefreshTokenTTL: config.Duration(time.Hour + 30*time.Minute),
		BusyTimeout:     config.Duration(5 * time.Second),
	}

	out, err := toml.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	outStr := string(out)
	if !strings.Contains(outStr, "'5m0s'") && !strings.Contains(outStr, `"5m0s"`) {
		t.Errorf("expected marshaled access_token_ttl to contain '5m0s' as string, got:\n%s", outStr)
	}

	var parsed doc
	if err := toml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal back: %v", err)
	}
	if parsed != original {
		t.Errorf("round trip mismatch:\n  got:  %+v\n  want: %+v", parsed, original)
	}
}

const validConfigTOML = `
[server]
listen_address = '127.0.0.1:30052'

[server.keepalive]
time = '10s'
timeout = '3s'
min_time = '5s'

[storage]
sqlite_path = 'data/authd.db'
busy_timeout = '5s'

[auth]
issuer = 'test'
access_token_ttl = '5m'
refresh_token_ttl = '1h30m'
max_users = 20
signing_key = 'test-key'
bootstrap_admin_username = 'admin'
bootstrap_admin_password = 'Admin123!'
bootstrap_admin_display_name = 'Admin'
bootstrap_admin_roles = ['admin']
`

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write toml: %v", err)
	}
	return path
}

func TestConfig_LoadParsesStringDurations(t *testing.T) {
	path := writeTempConfig(t, validConfigTOML)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if got, want := cfg.Storage.BusyTimeout.Std(), 5*time.Second; got != want {
		t.Errorf("busy_timeout: got %v, want %v", got, want)
	}
	if got, want := cfg.Auth.AccessTokenTTL.Std(), 5*time.Minute; got != want {
		t.Errorf("access_token_ttl: got %v, want %v", got, want)
	}
	if got, want := cfg.Auth.RefreshTokenTTL.Std(), time.Hour+30*time.Minute; got != want {
		t.Errorf("refresh_token_ttl: got %v, want %v", got, want)
	}
}

func TestConfig_LoadMissingFileErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing.toml")
	if _, err := config.Load(path); err == nil {
		t.Fatalf("expected error for missing config, got nil")
	}
}

func TestConfig_LoadRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name    string
		mutator func(s string) string
	}{
		{"empty signing_key", func(s string) string {
			return strings.Replace(s, "signing_key = 'test-key'", "signing_key = ''", 1)
		}},
		{"zero max_users", func(s string) string {
			return strings.Replace(s, "max_users = 20", "max_users = 0", 1)
		}},
		{"empty bootstrap_admin_username", func(s string) string {
			return strings.Replace(s, "bootstrap_admin_username = 'admin'", "bootstrap_admin_username = ''", 1)
		}},
		{"empty bootstrap_admin_roles", func(s string) string {
			return strings.Replace(s, "bootstrap_admin_roles = ['admin']", "bootstrap_admin_roles = []", 1)
		}},
		{"bad listen_address", func(s string) string {
			return strings.Replace(s, "listen_address = '127.0.0.1:30052'", "listen_address = 'not-a-host-port'", 1)
		}},
		{"refresh ttl < access ttl", func(s string) string {
			return strings.Replace(s, "refresh_token_ttl = '1h30m'", "refresh_token_ttl = '1m'", 1)
		}},
		{"zero busy_timeout", func(s string) string {
			return strings.Replace(s, "busy_timeout = '5s'", "busy_timeout = '0s'", 1)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempConfig(t, tc.mutator(validConfigTOML))
			if _, err := config.Load(path); err == nil {
				t.Errorf("expected validation error, got nil")
			}
		})
	}
}

func TestConfig_LoadRejectsUnknownField(t *testing.T) {
	// refresh_token_extend_on_refresh 已搬到 authd-settings.toml，舊欄位應被拒絕。
	tainted := strings.Replace(
		validConfigTOML,
		"signing_key = 'test-key'",
		"signing_key = 'test-key'\nrefresh_token_extend_on_refresh = true",
		1,
	)
	path := writeTempConfig(t, tainted)
	if _, err := config.Load(path); err == nil {
		t.Fatalf("expected error for unknown field, got nil")
	}
}
