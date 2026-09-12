package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCfg(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

const validHash = "pbkdf2-sha256$210000$AAAAAAAAAAAAAAAAAAAAAA$rvEjT4LYUNezLGLLxcKZpz7JDhLeEi3dvTIheBrzQyU"

func TestLoadDefaultsBaseURL(t *testing.T) {
	path := writeCfg(t, `{
		"auth": {"username": "admin", "password_hash": "`+validHash+`"}
	}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.BaseURL != DefaultBaseURL {
		t.Fatalf("expected default base url, got %q", cfg.BaseURL)
	}
}

func TestLoadNormalizesBaseURL(t *testing.T) {
	path := writeCfg(t, `{
		"auth": {"username": "admin", "password_hash": "`+validHash+`"},
		"base_url": "https://gw.example.com/"
	}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.BaseURL != "https://gw.example.com" {
		t.Fatalf("trailing slash must be trimmed, got %q", cfg.BaseURL)
	}
}

func TestValidateRejectsInvalid(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"missing username", `{"auth": {"password": "x"}}`},
		{"missing credentials", `{"auth": {"username": "a"}}`},
		{"both password forms", `{"auth": {"username": "a", "password": "x", "password_hash": "` + validHash + `"}}`},
		{"bad hash format", `{"auth": {"username": "a", "password_hash": "plaintext"}}`},
		{"tg missing token", `{"auth": {"username": "a", "password": "x"}, "telegram": {"enabled": true, "chat_id": "1"}}`},
		{"tg bad time", `{"auth": {"username": "a", "password": "x"}, "telegram": {"enabled": true, "bot_token": "t", "chat_id": "1", "daily_report_time": "25:00"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(writeCfg(t, tc.json)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestManagerReloadKeepsOldOnFailure(t *testing.T) {
	path := writeCfg(t, `{
		"auth": {"username": "admin", "password_hash": "`+validHash+`"},
		"base_url": "https://gw.example.com"
	}`)
	mgr, err := NewManager(path)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if mgr.Current().BaseURL != "https://gw.example.com" {
		t.Fatalf("unexpected initial config: %+v", mgr.Current())
	}

	// 写入非法配置 → Reload 失败且保留旧值
	if err := os.WriteFile(path, []byte(`{"auth": {}}`), 0o600); err != nil {
		t.Fatalf("write bad config: %v", err)
	}
	if err := mgr.Reload(); err == nil {
		t.Fatal("expected reload to fail on invalid config")
	}
	if mgr.Current().BaseURL != "https://gw.example.com" {
		t.Fatal("old config must be preserved on failed reload")
	}
}
