package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"emby-proxy-stat/internal/auth"
	"emby-proxy-stat/internal/caddylog"
	"emby-proxy-stat/internal/config"
	"emby-proxy-stat/internal/notify"
	"emby-proxy-stat/internal/store"
)

type testEnv struct {
	handler http.Handler
	cfgPath string
	sender  *notify.Sender
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	hash, err := auth.GeneratePasswordHash("s3cret-pass")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	cfgJSON := `{
		"auth": {"username": "admin", "password_hash": "` + hash + `"},
		"telegram": {"enabled": true, "bot_token": "tok", "chat_id": "42", "daily_report_time": "23:59"},
		"base_url": "https://auto.fleey.de"
	}`
	if err := writeConfig(cfgPath, cfgJSON); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfgMgr, err := config.NewManager(cfgPath)
	if err != nil {
		t.Fatalf("config manager: %v", err)
	}
	db, err := store.Open(filepath.Join(dir, "stats.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	sender := notify.NewSender(false, "", "")
	liveSessions := func() []caddylog.SessionInfo {
		return []caddylog.SessionInfo{{
			TargetHost: "node.example:8096", DeviceName: "TestTV", ItemID: "it1",
			LastSeenSecAgo: 5, Active: true,
		}}
	}
	handler := New(Deps{
		Config:       cfgMgr,
		Store:        db,
		Sessions:     auth.NewSessionManager(time.Hour),
		Limiter:      auth.NewLoginLimiter(),
		Sender:       sender,
		LiveSessions: liveSessions,
	})
	return &testEnv{handler: handler, cfgPath: cfgPath, sender: sender}
}

func writeConfig(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

// 端到端：健康检查 → 未登录 401 → 登录 → 带 Cookie 访问统计。
func TestAuthFlow(t *testing.T) {
	env := newTestEnv(t)

	health := httptest.NewRequest("GET", "/api/health", nil)
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, health)
	if rec.Code != 200 {
		t.Fatalf("health: %d", rec.Code)
	}

	unauth := httptest.NewRequest("GET", "/api/stats", nil)
	rec = httptest.NewRecorder()
	env.handler.ServeHTTP(rec, unauth)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("stats without session: %d", rec.Code)
	}

	// 直接在同一 handler 上模拟登录（避免起真服务器）
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "s3cret-pass"})
	req := httptest.NewRequest("POST", "/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	var loginResp struct {
		Success bool `json:"success"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &loginResp)
	if !loginResp.Success {
		t.Fatal("login should succeed")
	}

	cookie := rec.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, "auth_token=") || !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "Secure") {
		t.Fatalf("session cookie missing security attributes: %q", cookie)
	}
	token := strings.Split(strings.Split(cookie, ";")[0], "=")[1]

	authed := httptest.NewRequest("GET", "/api/stats", nil)
	authed.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
	rec = httptest.NewRecorder()
	env.handler.ServeHTTP(rec, authed)
	if rec.Code != 200 {
		t.Fatalf("stats with session: %d", rec.Code)
	}
	var stats map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("stats payload: %v", err)
	}
	// 前端契约字段必须存在
	for _, key := range []string{"today_plays", "total_plays", "today_traffic_fmt", "date", "status"} {
		if _, ok := stats[key]; !ok {
			t.Fatalf("stats response missing contract field %q", key)
		}
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	env := newTestEnv(t)
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "nope"})
	req := httptest.NewRequest("POST", "/api/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", rec.Code)
	}
}

func TestIndexServedWithNoStore(t *testing.T) {
	env := newTestEnv(t)
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Emby Proxy Toolbox") {
		t.Fatalf("index page broken: %d", rec.Code)
	}

	req404 := httptest.NewRequest("GET", "/nonexistent", nil)
	rec = httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req404)
	if rec.Code != 404 {
		t.Fatalf("unknown path: %d", rec.Code)
	}
}

// 新增端点契约：趋势 / 流量分布 / 实时会话（需登录）。
func TestNewEndpoints(t *testing.T) {
	env := newTestEnv(t)
	token := loginAndGetToken(t, env.handler)

	cases := []struct {
		path    string
		require []string
	}{
		{"/api/trend?days=7", []string{"points"}},
		{"/api/traffic-breakdown", []string{"date", "by_client", "by_host"}},
		{"/api/sessions", []string{"sessions", "active"}},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.path, nil)
			req.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
			rec := httptest.NewRecorder()
			env.handler.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("payload: %v", err)
			}
			for _, key := range tc.require {
				if _, ok := payload[key]; !ok {
					t.Fatalf("response missing %q: %v", key, payload)
				}
			}
		})
	}

	// 未登录一律 401
	req := httptest.NewRequest("GET", "/api/trend", nil)
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth trend: %d", rec.Code)
	}
}

// 从登录响应中提取会话令牌。
func loginAndGetToken(t *testing.T, h http.Handler) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "s3cret-pass"})
	req := httptest.NewRequest("POST", "/api/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("login: %d", rec.Code)
	}
	cookie := rec.Header().Get("Set-Cookie")
	return strings.Split(strings.Split(cookie, ";")[0], "=")[1]
}
