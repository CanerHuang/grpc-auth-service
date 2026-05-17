package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"authd/internal/config"
)

func TestLoadSettings_CreatesDefaultWhenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.settings.toml")

	store, err := config.LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if !store.RefreshTokenExtendOnRefresh() {
		t.Errorf("expected default RefreshTokenExtendOnRefresh=true")
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected settings file to be created at %s: %v", path, err)
	}
}

func TestLoadSettings_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.settings.toml")

	store, err := config.LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if err := store.SetRefreshTokenExtendOnRefresh(false); err != nil {
		t.Fatalf("Set: %v", err)
	}

	reloaded, err := config.LoadSettings(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.RefreshTokenExtendOnRefresh() {
		t.Errorf("expected persisted value to be false")
	}
}

func TestLoadSettings_PreservesExistingValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.settings.toml")
	if err := os.WriteFile(path, []byte("refresh_token_extend_on_refresh = false\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	store, err := config.LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if store.RefreshTokenExtendOnRefresh() {
		t.Errorf("expected loaded value to be false")
	}
}
