package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"
)

func generateToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func isAuthenticated(r *http.Request) bool {
	cookie, err := r.Cookie("auth_token")
	if err != nil || cookie.Value == "" {
		return false
	}
	sessionMu.Lock()
	defer sessionMu.Unlock()
	expiry, exists := activeSessions[cookie.Value]
	if !exists || time.Now().After(expiry) {
		delete(activeSessions, cookie.Value)
		return false
	}
	return true
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	clientKey := loginClientKey(r)
	if retryAfter := loginRetryAfter(clientKey); retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "登录尝试过于频繁，请稍后重试"})
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误"})
		return
	}

	cfg := loadConfig()
	if req.Username == cfg.Auth.Username && verifyPassword(req.Password, cfg) {
		clearLoginFailures(clientKey)
		token, err := generateToken()
		if err != nil {
			log.Printf("[Auth Error] token generation failed: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "登录服务暂不可用"})
			return
		}
		expiry := time.Now().Add(30 * 24 * time.Hour)
		sessionMu.Lock()
		activeSessions[token] = expiry
		sessionMu.Unlock()
		http.SetCookie(w, &http.Cookie{
			Name:     "auth_token",
			Value:    token,
			Path:     "/",
			Expires:  expiry,
			MaxAge:   30 * 24 * 60 * 60,
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
		})
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "message": "登录成功"})
		return
	}
	recordLoginFailure(clientKey)
	writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"success": false, "message": "账号或密码错误"})
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	if cookie, err := r.Cookie("auth_token"); err == nil && cookie.Value != "" {
		sessionMu.Lock()
		delete(activeSessions, cookie.Value)
		sessionMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_token",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	if !isAuthenticated(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}
	stats, err := getStats()
	if err != nil {
		log.Printf("[Stats Error] %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "统计服务暂不可用"})
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func handleTestTG(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	if !isAuthenticated(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}
	stats, err := getStats()
	if err != nil {
		log.Printf("[Stats Error] %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "统计服务暂不可用"})
		return
	}
	now := businessNow()
	msg := fmt.Sprintf(
		"🔔 <b>Emby 网关 Telegram 连通测试</b>\n"+
			"━━━━━━━━━━━━━━━━━━\n"+
			"📅 <b>测试时间</b>：<code>%s</code>\n\n"+
			"📊 <b>【实时网关概览】</b>\n"+
			"• 🎬 <b>今日播放</b>：<code>%d</code> 次\n"+
			"• 🌐 <b>今日流量</b>：<code>%s</code>\n"+
			"• 👥 <b>活跃设备</b>：<code>%d</code> 个独立客户端\n"+
			"• 🖥️ <b>覆盖节点</b>：<code>%d</code> 个目标服务器\n\n"+
			"📈 <b>【历史全量汇总】</b>\n"+
			"• 🎬 <b>累计播放</b>：<code>%d</code> 次\n"+
			"• 💾 <b>累计总流量</b>：<code>%s</code>\n"+
			"• 📱 <b>累计服务设备</b>：<code>%d</code> 个\n\n"+
			"<blockquote>🟢 <b>引擎核心</b>：Go Core (Zero-CGO)\n"+
			"⏰ <b>每日播报</b>：按配置时间自动推送\n"+
			"🔗 <b>管理控制台</b>：<a href=\"%s\">%s</a></blockquote>",
		now.Format("2006-01-02 15:04:05"),
		stats.TodayPlays, stats.TodayTrafficFmt, stats.TodayClients, stats.TodayHosts,
		stats.TotalPlays, stats.TotalTrafficFmt, stats.TotalClients,
		publicURL(), publicURL(),
	)
	if !sendTelegramMessage(msg) {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"success": false, "message": "Telegram 推送失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}
