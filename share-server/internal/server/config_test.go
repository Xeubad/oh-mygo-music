package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadFrom(t *testing.T, env map[string]string) (Config, error) {
	t.Helper()
	return LoadConfig(func(key string) string { return env[key] })
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadFrom(t, map[string]string{
		"SHARE_TOKEN":    "token-123",
		"SHARE_DATA_DIR": t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Listen != defaultListen {
		t.Errorf("Listen = %q, want %q", cfg.Listen, defaultListen)
	}
	if cfg.PublicOrigin != defaultPublicOrigin {
		t.Errorf("PublicOrigin = %q, want %q", cfg.PublicOrigin, defaultPublicOrigin)
	}
	if cfg.TTL != defaultTTL {
		t.Errorf("TTL = %s, want %s", cfg.TTL, defaultTTL)
	}
	if !filepath.IsAbs(cfg.DataDir) {
		t.Errorf("DataDir = %q, want an absolute path", cfg.DataDir)
	}
}

func TestLoadConfigRequiresToken(t *testing.T) {
	_, err := loadFrom(t, map[string]string{"SHARE_DATA_DIR": t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "SHARE_TOKEN") {
		t.Fatalf("err = %v, want a missing token error", err)
	}
}

func TestLoadConfigRejectsBothTokenSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("file-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadFrom(t, map[string]string{
		"SHARE_TOKEN":      "inline",
		"SHARE_TOKEN_FILE": path,
		"SHARE_DATA_DIR":   t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected an error when both token sources are set")
	}
}

func TestLoadConfigReadsTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("  file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadFrom(t, map[string]string{
		"SHARE_TOKEN_FILE": path,
		"SHARE_DATA_DIR":   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Token != "file-token" {
		t.Errorf("Token = %q, want %q", cfg.Token, "file-token")
	}
}

func TestLoadConfigOriginRules(t *testing.T) {
	base := map[string]string{"SHARE_TOKEN": "token-123", "SHARE_DATA_DIR": t.TempDir()}
	for _, origin := range []string{
		"https://share.example.com",
		"http://localhost:9000",
		"http://127.0.0.1:8080",
		"https://share.example.com/",
	} {
		env := map[string]string{"SHARE_PUBLIC_ORIGIN": origin}
		for key, value := range base {
			env[key] = value
		}
		if _, err := loadFrom(t, env); err != nil {
			t.Errorf("origin %q: unexpected error %v", origin, err)
		}
	}
	for _, origin := range []string{
		"http://share.example.com",
		"https://share.example.com/path",
		"https://user:pass@share.example.com",
		"ftp://share.example.com",
		"not-a-url",
	} {
		env := map[string]string{"SHARE_PUBLIC_ORIGIN": origin}
		for key, value := range base {
			env[key] = value
		}
		if _, err := loadFrom(t, env); err == nil {
			t.Errorf("origin %q: expected an error", origin)
		}
	}
}

func TestLoadConfigTTL(t *testing.T) {
	base := map[string]string{"SHARE_TOKEN": "token-123", "SHARE_DATA_DIR": t.TempDir()}
	env := map[string]string{"SHARE_TTL": "90m"}
	for key, value := range base {
		env[key] = value
	}
	cfg, err := loadFrom(t, env)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.TTL != 90*time.Minute {
		t.Errorf("TTL = %s, want 90m", cfg.TTL)
	}
	for _, value := range []string{"-1h", "0s", "soon", "8761h"} {
		env := map[string]string{"SHARE_TTL": value}
		for key, baseValue := range base {
			env[key] = baseValue
		}
		if _, err := loadFrom(t, env); err == nil {
			t.Errorf("TTL %q: expected an error", value)
		}
	}
}

func TestLoadConfigListen(t *testing.T) {
	_, err := loadFrom(t, map[string]string{
		"SHARE_TOKEN":    "token-123",
		"SHARE_DATA_DIR": t.TempDir(),
		"SHARE_LISTEN":   "not-an-address",
	})
	if err == nil {
		t.Fatal("expected an error for an invalid listen address")
	}
}
