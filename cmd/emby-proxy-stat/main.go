package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed index.html
var indexHTML []byte

var (
	configPathFlag = flag.String("config", "/opt/emby-proxy-stat/config.json", "Path to config file")
	dbPathFlag     = flag.String("db", "/opt/emby-proxy-stat/data/stats.db", "Path to sqlite database")
	logPathFlag    = flag.String("log", "/var/log/caddy/emby-gateway.log", "Path to Caddy access log file")
	portFlag       = flag.Int("port", 8999, "HTTP listen port")
)

const (
	DebounceSeconds = 300
)

type Config struct {
	Auth struct {
		Username string `json:"username"`
		Password string `json:"password"`
	} `json:"auth"`
	Telegram struct {
		Enabled         bool   `json:"enabled"`
		BotToken        string `json:"bot_token"`
		ChatID          string `json:"chat_id"`
		DailyReportTime string `json:"daily_report_time"`
	} `json:"telegram"`
	CaddyLogPath string `json:"caddy_log_path"`
}

type StatsResponse struct {
	TodayPlays      int64  `json:"today_plays"`
	TotalPlays      int64  `json:"total_plays"`
	TodayBytes      int64  `json:"today_bytes"`
	TotalBytes      int64  `json:"total_bytes"`
	TodayTrafficFmt string `json:"today_traffic_fmt"`
	TotalTrafficFmt string `json:"total_traffic_fmt"`
	Date            string `json:"date"`
	Status          string `json:"status"`
}

type TrafficEntry struct {
	Bytes    int64
	Requests int64
}

var (
	playPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)/Videos/([a-zA-Z0-9\-_]+)/(?:stream|original|master|main)`),
		regexp.MustCompile(`(?i)/Items/([a-zA-Z0-9\-_]+)/PlaybackInfo`),
		regexp.MustCompile(`(?i)/Audio/([a-zA-Z0-9\-_]+)/stream`),
		regexp.MustCompile(`(?i)/Sessions/Playing`),
	}
	targetHostPattern = regexp.MustCompile(`^/https?:/*([A-Za-z0-9.\-_:]+)`)

	db            *sql.DB
	dbMu          sync.Mutex
	cfgMu         sync.RWMutex
	currentConfig Config

	recentPlays = make(map[string]int64)
	recentMu    sync.Mutex

	trafficBuffer = make(map[string]*TrafficEntry)
	trafficMu     sync.Mutex

	activeSessions = make(map[string]time.Time)
	sessionMu      sync.Mutex
)

func formatBytes(b int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
		tb = 1024 * gb
	)
	fb := float64(b)
	switch {
	case b >= tb:
		return fmt.Sprintf("%.2f TB", fb/float64(tb))
	case b >= gb:
		return fmt.Sprintf("%.2f GB", fb/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.2f MB", fb/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.2f KB", fb/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func loadConfig() Config {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return currentConfig
}

func reloadConfig() error {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	data, err := os.ReadFile(*configPathFlag)
	if err != nil {
		return err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	currentConfig = cfg
	return nil
}

func getEffectiveLogPath() string {
	cfg := loadConfig()
	if cfg.CaddyLogPath != "" {
		return cfg.CaddyLogPath
	}
	return *logPathFlag
}

func initDB() error {
	if err := os.MkdirAll(filepath.Dir(*dbPathFlag), 0755); err != nil {
		return err
	}
	var err error
	db, err = sql.Open("sqlite", *dbPathFlag+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)

	schema := `
	CREATE TABLE IF NOT EXISTS play_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		played_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		play_date TEXT,
		client_ip TEXT,
		target_host TEXT,
		item_id TEXT,
		uri TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_play_date ON play_events(play_date);
	CREATE INDEX IF NOT EXISTS idx_played_at ON play_events(played_at);

	CREATE TABLE IF NOT EXISTS daily_traffic (
		date TEXT PRIMARY KEY,
		bytes INTEGER DEFAULT 0,
		requests INTEGER DEFAULT 0,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	`
	_, err = db.Exec(schema)
	return err
}

func recordPlay(clientIP, targetHost, itemID, uri string) {
	now := time.Now().Unix()
	todayStr := time.Now().Format("2006-01-02")
	dedupKey := fmt.Sprintf("%s:%s:%s", clientIP, targetHost, itemID)

	recentMu.Lock()
	for k, v := range recentPlays {
		if now-v > DebounceSeconds {
			delete(recentPlays, k)
		}
	}
	if last, ok := recentPlays[dedupKey]; ok && now-last < DebounceSeconds {
		recentMu.Unlock()
		return
	}
	recentPlays[dedupKey] = now
	recentMu.Unlock()

	dbMu.Lock()
	defer dbMu.Unlock()
	_, err := db.Exec(`
		INSERT INTO play_events (play_date, client_ip, target_host, item_id, uri)
		VALUES (?, ?, ?, ?, ?)
	`, todayStr, clientIP, targetHost, itemID, uri)
	if err != nil {
		log.Printf("[DB Error recordPlay] %v", err)
	}
}

func addTraffic(dateStr string, sizeBytes int64) {
	trafficMu.Lock()
	defer trafficMu.Unlock()
	entry, ok := trafficBuffer[dateStr]
	if !ok {
		entry = &TrafficEntry{}
		trafficBuffer[dateStr] = entry
	}
	entry.Bytes += sizeBytes
	entry.Requests++
}

func flushTrafficWorker() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		trafficMu.Lock()
		if len(trafficBuffer) == 0 {
			trafficMu.Unlock()
			continue
		}
		batch := make(map[string]TrafficEntry, len(trafficBuffer))
		for k, v := range trafficBuffer {
			batch[k] = *v
		}
		trafficBuffer = make(map[string]*TrafficEntry)
		trafficMu.Unlock()

		dbMu.Lock()
		tx, err := db.Begin()
		if err == nil {
			stmt, err := tx.Prepare(`
				INSERT INTO daily_traffic (date, bytes, requests, updated_at)
				VALUES (?, ?, ?, CURRENT_TIMESTAMP)
				ON CONFLICT(date) DO UPDATE SET
					bytes = bytes + excluded.bytes,
					requests = requests + excluded.requests,
					updated_at = CURRENT_TIMESTAMP
			`)
			if err == nil {
				for dStr, entry := range batch {
					_, _ = stmt.Exec(dStr, entry.Bytes, entry.Requests)
				}
				_ = stmt.Close()
			}
			_ = tx.Commit()
		}
		dbMu.Unlock()
	}
}

func getStats() StatsResponse {
	todayStr := time.Now().Format("2006-01-02")
	var todayPlays, totalPlays, todayBytes, totalBytes int64

	trafficMu.Lock()
	if entry, ok := trafficBuffer[todayStr]; ok {
		todayBytes += entry.Bytes
		totalBytes += entry.Bytes
	}
	trafficMu.Unlock()

	dbMu.Lock()
	_ = db.QueryRow("SELECT COUNT(*) FROM play_events WHERE play_date = ?", todayStr).Scan(&todayPlays)
	_ = db.QueryRow("SELECT COUNT(*) FROM play_events").Scan(&totalPlays)

	var dbTodayBytes sql.NullInt64
	_ = db.QueryRow("SELECT bytes FROM daily_traffic WHERE date = ?", todayStr).Scan(&dbTodayBytes)
	if dbTodayBytes.Valid {
		todayBytes += dbTodayBytes.Int64
	}

	var dbTotalBytes sql.NullInt64
	_ = db.QueryRow("SELECT SUM(bytes) FROM daily_traffic").Scan(&dbTotalBytes)
	if dbTotalBytes.Valid {
		totalBytes += dbTotalBytes.Int64
	}
	dbMu.Unlock()

	return StatsResponse{
		TodayPlays:      todayPlays,
		TotalPlays:      totalPlays,
		TodayBytes:      todayBytes,
		TotalBytes:      totalBytes,
		TodayTrafficFmt: formatBytes(todayBytes),
		TotalTrafficFmt: formatBytes(totalBytes),
		Date:            todayStr,
		Status:          "online",
	}
}

func sendTelegramMessage(text string) bool {
	cfg := loadConfig()
	if !cfg.Telegram.Enabled || cfg.Telegram.BotToken == "" || cfg.Telegram.ChatID == "" {
		return false
	}
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", cfg.Telegram.BotToken)
	payload := map[string]interface{}{
		"chat_id":                  cfg.Telegram.ChatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[TG Error] %v", err)
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func telegramSchedulerWorker() {
	var lastSentDate string
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		todayStr := now.Format("2006-01-02")
		cfg := loadConfig()
		if cfg.Telegram.Enabled {
			targetTime := cfg.Telegram.DailyReportTime
			if targetTime == "" {
				targetTime = "23:59"
			}
			currentHM := now.Format("15:04")
			if currentHM == targetTime && lastSentDate != todayStr {
				stats := getStats()
				msg := fmt.Sprintf(
					"📊 <b>Emby 网关每日数据播报 (Go Edition)</b>\n\n"+
						"📅 <b>统计日期</b>：%s\n"+
						"🎬 <b>今日播放</b>：<code>%d</code> 次\n"+
						"🌐 <b>今日流量</b>：<code>%s</code>\n\n"+
						"📈 <b>累计播放</b>：<code>%d</code> 次\n"+
						"💾 <b>累计流量</b>：<code>%s</code>\n\n"+
						"⚡ <b>网关状态</b>：Go 核心引擎运行正常",
					todayStr, stats.TodayPlays, stats.TodayTrafficFmt,
					stats.TotalPlays, stats.TotalTrafficFmt,
				)
				if sendTelegramMessage(msg) {
					lastSentDate = todayStr
					log.Printf("[TG Scheduler] Daily report sent for %s", todayStr)
				}
			}
		}
	}
}

type CaddyLogEntry struct {
	Request struct {
		ClientIP string              `json:"client_ip"`
		RemoteIP string              `json:"remote_ip"`
		URI      string              `json:"uri"`
		Headers  map[string][]string `json:"headers"`
	} `json:"request"`
	Status int   `json:"status"`
	Size   int64 `json:"size"`
}

func matchPlayEvent(uri string) string {
	for _, pattern := range playPatterns {
		matches := pattern.FindStringSubmatch(uri)
		if len(matches) > 1 {
			return matches[1]
		} else if len(matches) == 1 {
			return "session"
		}
	}
	return ""
}

func extractTargetHost(uri string) string {
	matches := targetHostPattern.FindStringSubmatch(uri)
	if len(matches) > 1 {
		return matches[1]
	}
	return "unknown"
}

func logTailWorker() {
	for {
		activeLogPath := getEffectiveLogPath()
		file, err := os.Open(activeLogPath)
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}

		fi, err := file.Stat()
		if err != nil {
			file.Close()
			time.Sleep(1 * time.Second)
			continue
		}

		_, _ = file.Seek(0, io.SeekEnd)
		reader := bufio.NewReader(file)

		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				if err == io.EOF {
					time.Sleep(200 * time.Millisecond)
					newFi, statErr := os.Stat(activeLogPath)
					if statErr != nil || !os.SameFile(fi, newFi) {
						file.Close()
						break
					}
					continue
				}
				file.Close()
				break
			}

			var entry CaddyLogEntry
			if jsonErr := json.Unmarshal(line, &entry); jsonErr != nil {
				continue
			}

			uri := entry.Request.URI
			if strings.HasPrefix(uri, "/api/") || uri == "/" || uri == "/index.html" {
				continue
			}

			todayStr := time.Now().Format("2006-01-02")
			if entry.Size > 0 {
				addTraffic(todayStr, entry.Size)
			}

			if entry.Status < 200 || entry.Status >= 400 {
				continue
			}

			itemID := matchPlayEvent(uri)
			if itemID != "" {
				clientIP := entry.Request.ClientIP
				if clientIP == "" {
					clientIP = entry.Request.RemoteIP
				}
				if xff, ok := entry.Request.Headers["X-Forwarded-For"]; ok && len(xff) > 0 {
					clientIP = strings.TrimSpace(strings.Split(xff[0], ",")[0])
				}
				clientIP = strings.Split(clientIP, ":")[0]
				targetHost := extractTargetHost(uri)
				recordPlay(clientIP, targetHost, itemID, uri)
			}
		}
	}
}

func generateToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
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
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	_ = json.Unmarshal(body, &req)

	cfg := loadConfig()
	if req.Username == cfg.Auth.Username && req.Password == cfg.Auth.Password {
		token := generateToken()
		expiry := time.Now().Add(30 * 24 * time.Hour)
		sessionMu.Lock()
		activeSessions[token] = expiry
		sessionMu.Unlock()

		http.SetCookie(w, &http.Cookie{
			Name:     "auth_token",
			Value:    token,
			Path:     "/",
			Expires:  expiry,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "登录成功",
		})
	} else {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "账号或密码错误",
		})
	}
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
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
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	if !isAuthenticated(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized"})
		return
	}
	stats := getStats()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(stats)
}

func handleTestTG(w http.ResponseWriter, r *http.Request) {
	if !isAuthenticated(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized"})
		return
	}
	stats := getStats()
	todayStr := time.Now().Format("2006-01-02")
	msg := fmt.Sprintf(
		"🔔 <b>Emby 网关 Telegram 连通测试 (Go Edition)</b>\n\n"+
			"📅 <b>当前日期</b>：%s\n"+
			"🎬 <b>今日播放</b>：<code>%d</code> 次\n"+
			"🌐 <b>今日流量</b>：<code>%s</code>\n"+
			"📈 <b>累计播放</b>：<code>%d</code> 次\n"+
			"💾 <b>累计流量</b>：<code>%s</code>\n\n"+
			"⚡ <b>状态</b>：Go 核心引擎运行正常，每日 23:59 自动推送！",
		todayStr, stats.TodayPlays, stats.TodayTrafficFmt,
		stats.TotalPlays, stats.TotalTrafficFmt,
	)
	ok := sendTelegramMessage(msg)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"success": ok})
}

func main() {
	flag.Parse()

	if err := reloadConfig(); err != nil {
		log.Printf("[Warn] Failed to load config: %v", err)
	}
	if err := initDB(); err != nil {
		log.Fatalf("[Fatal] Failed to init DB: %v", err)
	}
	defer db.Close()

	go logTailWorker()
	go flushTrafficWorker()
	go telegramSchedulerWorker()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", handleLogin)
	mux.HandleFunc("/api/logout", handleLogout)
	mux.HandleFunc("/api/stats", handleStats)
	mux.HandleFunc("/api/test-tg", handleTestTG)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","engine":"go"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(indexHTML)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("OK"))
	})

	server := &http.Server{
		Addr:         fmt.Sprintf("127.0.0.1:%d", *portFlag),
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	log.Printf("[Go Stats Server] Running on 127.0.0.1:%d", *portFlag)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[Fatal] Server error: %v", err)
	}
}
