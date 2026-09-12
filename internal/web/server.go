// Package web 提供仪表盘 HTTP 服务：静态前端、登录鉴权与统计 API。
//
// 路由契约（字段名与前端 index.html 绑定，不得变更）：
//
//	GET  /              仪表盘页面（二进制内嵌）
//	POST /api/login     登录，签发 30 天 HttpOnly Cookie
//	POST /api/logout    登出并作废会话
//	GET  /api/stats     今日与历史统计
//	GET  /api/clients   今日播放明细
//	POST /api/test-tg   触发一次 Telegram 测试推送
//	GET  /api/health    存活探针
package web

import (
	_ "embed"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"emby-proxy-stat/internal/auth"
	"emby-proxy-stat/internal/caddylog"
	"emby-proxy-stat/internal/clock"
	"emby-proxy-stat/internal/config"
	"emby-proxy-stat/internal/netutil"
	"emby-proxy-stat/internal/notify"
	"emby-proxy-stat/internal/store"
)

//go:embed index.html
var indexHTML []byte

const sessionCookieName = "auth_token"
const sessionTTL = 30 * 24 * time.Hour

// LiveSessionsProvider 返回当前观看会话快照。
type LiveSessionsProvider func() []caddylog.SessionInfo

// Deps 聚合 web 层依赖。
type Deps struct {
	Config       *config.Manager
	Store        *store.Store
	Sessions     *auth.SessionManager
	Limiter      *auth.LoginLimiter
	Sender       *notify.Sender
	LiveSessions LiveSessionsProvider
}

// New 构造完整路由。
func New(d Deps) http.Handler {
	mux := http.NewServeMux()
	s := &server{d: d}

	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/stats", s.requireSession(s.handleStats))
	mux.HandleFunc("GET /api/clients", s.requireSession(s.handleClients))
	mux.HandleFunc("GET /api/trend", s.requireSession(s.handleTrend))
	mux.HandleFunc("GET /api/traffic-breakdown", s.requireSession(s.handleTrafficBreakdown))
	mux.HandleFunc("GET /api/sessions", s.requireSession(s.handleSessions))
	mux.HandleFunc("POST /api/test-tg", s.requireSession(s.handleTestTG))
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /index.html", s.handleIndex)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "Not Found")
	})
	return mux
}

type server struct {
	d Deps
}

// requireSession 为需要登录的接口统一做 Cookie 鉴权。
func (s *server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || !s.d.Sessions.Valid(cookie.Value) {
			writeError(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
		next(w, r)
	}
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(indexHTML)
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "engine": "go"})
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	clientKey := netutil.ClientKeyFromRequest(r)
	if retryAfter := s.d.Limiter.RetryAfter(clientKey); retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "登录尝试过于频繁，请稍后重试"})
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}

	cfg := s.d.Config.Current()
	if req.Username != cfg.Auth.Username ||
		!auth.VerifyPassword(req.Password, cfg.Auth.PasswordHash, cfg.Auth.Password) {
		s.d.Limiter.RecordFailure(clientKey)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "账号或密码错误"})
		return
	}

	s.d.Limiter.Reset(clientKey)
	token, expires, err := s.d.Sessions.Create()
	if err != nil {
		log.Printf("[Auth Error] token generation failed: %v", err)
		writeError(w, http.StatusInternalServerError, "登录服务暂不可用")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "登录成功"})
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		s.d.Sessions.Destroy(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.d.Store.Stats(clock.Today())
	if err != nil {
		log.Printf("[Stats Error] %v", err)
		writeError(w, http.StatusServiceUnavailable, "统计服务暂不可用")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *server) handleClients(w http.ResponseWriter, r *http.Request) {
	clients, err := s.d.Store.TodayClients(clock.Today())
	if err != nil {
		log.Printf("[Clients Error] %v", err)
		writeError(w, http.StatusServiceUnavailable, "客户端查询服务暂不可用")
		return
	}
	writeJSON(w, http.StatusOK, clients)
}

// handleTrend 返回最近 N 天（默认 30，上限 365）播放与流量趋势。
func (s *server) handleTrend(w http.ResponseWriter, r *http.Request) {
	days := 30
	if raw := r.URL.Query().Get("days"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			days = n
		}
	}
	points, err := s.d.Store.Trend(days)
	if err != nil {
		log.Printf("[Trend Error] %v", err)
		writeError(w, http.StatusServiceUnavailable, "趋势查询服务暂不可用")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"points": points})
}

func (s *server) handleTrafficBreakdown(w http.ResponseWriter, r *http.Request) {
	breakdown, err := s.d.Store.Breakdown(clock.Today())
	if err != nil {
		log.Printf("[Breakdown Error] %v", err)
		writeError(w, http.StatusServiceUnavailable, "流量分布查询暂不可用")
		return
	}
	writeJSON(w, http.StatusOK, breakdown)
}

func (s *server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if s.d.LiveSessions == nil {
		writeJSON(w, http.StatusOK, map[string]any{"sessions": []any{}, "active": 0})
		return
	}
	all := s.d.LiveSessions()
	active := 0
	for _, sess := range all {
		if sess.Active {
			active++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": all, "active": active})
}

func (s *server) handleTestTG(w http.ResponseWriter, r *http.Request) {
	stats, err := s.d.Store.Stats(clock.Today())
	if err != nil {
		log.Printf("[Stats Error] %v", err)
		writeError(w, http.StatusServiceUnavailable, "统计服务暂不可用")
		return
	}
	if !s.d.Sender.Send(notify.BuildTestReport(stats, clock.Now(), s.d.Config.Current().BaseURL)) {
		writeJSON(w, http.StatusBadGateway, map[string]any{"success": false, "message": "Telegram 推送失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// SessionTTL 暴露会话有效期供部署方对齐 Cookie MaxAge。
func SessionTTL() time.Duration { return sessionTTL }
