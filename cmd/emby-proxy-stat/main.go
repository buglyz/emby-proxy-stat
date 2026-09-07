package main

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
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
	configPathFlag   = flag.String("config", "/opt/emby-proxy-stat/config.json", "Path to config file")
	dbPathFlag       = flag.String("db", "/opt/emby-proxy-stat/data/stats.db", "Path to sqlite database")
	logPathFlag      = flag.String("log", "/var/log/caddy/auto.fleey.de.log", "Path to Caddy access log file")
	portFlag         = flag.Int("port", 8999, "HTTP listen port")
	passwordHashFlag = flag.Bool("generate-password-hash", false, "Read a password from stdin and print a PBKDF2 hash")
)

const DebounceSeconds = 1800

type Config struct {
	Auth struct {
		Username     string `json:"username"`
		Password     string `json:"password,omitempty"`
		PasswordHash string `json:"password_hash,omitempty"`
	} `json:"auth"`
	Telegram struct {
		Enabled         bool   `json:"enabled"`
		BotToken        string `json:"bot_token"`
		ChatID          string `json:"chat_id"`
		DailyReportTime string `json:"daily_report_time"`
	} `json:"telegram"`
	CaddyLogPath string `json:"caddy_log_path"`
	PublicURL    string `json:"public_url"`
}

type StatsResponse struct {
	TodayPlays      int64  `json:"today_plays"`
	TotalPlays      int64  `json:"total_plays"`
	TodayBytes      int64  `json:"today_bytes"`
	TotalBytes      int64  `json:"total_bytes"`
	TodayTrafficFmt string `json:"today_traffic_fmt"`
	TotalTrafficFmt string `json:"total_traffic_fmt"`
	TodayClients    int64  `json:"today_clients"`
	TotalClients    int64  `json:"total_clients"`
	TodayHosts      int64  `json:"today_hosts"`
	TotalHosts      int64  `json:"total_hosts"`
	Date            string `json:"date"`
	Status          string `json:"status"`
}

type TrafficEntry struct {
	Bytes    int64
	Requests int64
}

type ActiveStream struct {
	ItemID string
	URI    string
	SeenAt time.Time
}

var (
	videoStreamPattern = regexp.MustCompile(`(?i)/Videos/([a-zA-Z0-9\-_]+)/(?:stream|original|master|main|\w+\.\w+)`)
	audioStreamPattern = regexp.MustCompile(`(?i)/Audio/([a-zA-Z0-9\-_]+)/stream`)
	progressPattern    = regexp.MustCompile(`(?i)/Sessions/Playing/Progress`)
	targetHostPattern  = regexp.MustCompile(`^/https?:/*([A-Za-z0-9.\-_:]+)`)

	db            *sql.DB
	dbMu          sync.Mutex
	cfgMu         sync.RWMutex
	currentConfig Config

	activeStreams  = make(map[string]ActiveStream)
	streamMu       sync.Mutex
	recentPlays    = make(map[string]int64)
	recentMu       sync.Mutex
	trafficBuffer  = make(map[string]*TrafficEntry)
	trafficMu      sync.Mutex
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

func publicURL() string {
	value := strings.TrimRight(strings.TrimSpace(loadConfig().PublicURL), "/")
	if value != "" {
		return value
	}
	return "https://auto.fleey.de"
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
	if err := validateConfig(cfg); err != nil {
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

func recordPlay(clientIP, targetHost, itemID, uri string, logTime time.Time) error {
	now := logTime.Unix()
	playedAtStr := logTime.Format("2006-01-02 15:04:05")
	todayStr := logTime.Format("2006-01-02")
	dedupKey := fmt.Sprintf("%s:%s:%s", clientIP, targetHost, itemID)

	recentMu.Lock()
	for key, value := range recentPlays {
		if now-value > DebounceSeconds {
			delete(recentPlays, key)
		}
	}
	if last, ok := recentPlays[dedupKey]; ok && now-last < DebounceSeconds {
		recentMu.Unlock()
		return nil
	}
	recentMu.Unlock()

	dbMu.Lock()
	_, err := db.Exec(`
		INSERT INTO play_events (played_at, play_date, client_ip, target_host, item_id, uri)
		VALUES (?, ?, ?, ?, ?, ?)
	`, playedAtStr, todayStr, clientIP, targetHost, itemID, uri)
	dbMu.Unlock()
	if err != nil {
		return err
	}
	recentMu.Lock()
	recentPlays[dedupKey] = now
	recentMu.Unlock()
	return nil
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
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Panic Recover] flushTrafficWorker: %v", r)
			go func() {
				time.Sleep(time.Second)
				flushTrafficWorker()
			}()
		}
	}()
	ticker := time.NewTicker(5 * time.Second)
	cleanTicker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	defer cleanTicker.Stop()
	for {
		select {
		case <-cleanTicker.C:
			now := time.Now()
			streamMu.Lock()
			for key, value := range activeStreams {
				if now.Sub(value.SeenAt) > 10*time.Minute {
					delete(activeStreams, key)
				}
			}
			streamMu.Unlock()
			recentMu.Lock()
			nowUnix := now.Unix()
			for key, value := range recentPlays {
				if nowUnix-value > DebounceSeconds {
					delete(recentPlays, key)
				}
			}
			recentMu.Unlock()
			sessionMu.Lock()
			for key, value := range activeSessions {
				if now.After(value) {
					delete(activeSessions, key)
				}
			}
			sessionMu.Unlock()
		case <-ticker.C:
			trafficMu.Lock()
			if len(trafficBuffer) == 0 {
				trafficMu.Unlock()
				continue
			}
			batch := make(map[string]TrafficEntry, len(trafficBuffer))
			for key, value := range trafficBuffer {
				batch[key] = *value
			}
			trafficBuffer = make(map[string]*TrafficEntry)
			trafficMu.Unlock()
			if err := persistTrafficBatch(batch); err != nil {
				restoreTrafficBatch(batch)
				log.Printf("[DB Error traffic batch] %v; batch restored", err)
			}
		}
	}
}
