// Package config 负责加载与校验 emby-proxy-stat 的 JSON 配置文件。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"emby-proxy-stat/internal/auth"
)

// DefaultBaseURL 是未配置 base_url 时的网关地址，保持与历史部署一致。
const DefaultBaseURL = "https://auto.fleey.de"

// DefaultRetentionDays 是未配置 retention_days 时的默认保留天数。
const DefaultRetentionDays = 365

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
	BaseURL      string `json:"base_url"`
	// RetentionDays 是播放与流量记录的保留天数；未设置默认 365，
	// 负数表示永久保留（不做清理）。
	RetentionDays int `json:"retention_days"`
}

// Load 读取、校验并补全配置（BaseURL 缺省、去尾部斜杠）。
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/")
	if cfg.RetentionDays == 0 {
		cfg.RetentionDays = DefaultRetentionDays
	}
	return cfg, nil
}

// Validate 校验配置完整性与格式，问题在启动时即暴露。
func Validate(cfg Config) error {
	if strings.TrimSpace(cfg.Auth.Username) == "" {
		return fmt.Errorf("auth.username is required")
	}
	if cfg.Auth.PasswordHash == "" && cfg.Auth.Password == "" {
		return fmt.Errorf("auth.password_hash or auth.password is required")
	}
	if cfg.Auth.PasswordHash != "" {
		if cfg.Auth.Password != "" {
			return fmt.Errorf("auth.password and auth.password_hash cannot both be set")
		}
		if err := auth.ValidatePasswordHash(cfg.Auth.PasswordHash); err != nil {
			return fmt.Errorf("invalid auth.password_hash: %w", err)
		}
	}
	if cfg.Telegram.Enabled {
		if cfg.Telegram.BotToken == "" || cfg.Telegram.ChatID == "" {
			return fmt.Errorf("telegram.bot_token and telegram.chat_id are required when Telegram is enabled")
		}
		if cfg.Telegram.DailyReportTime != "" {
			if _, err := time.Parse("15:04", cfg.Telegram.DailyReportTime); err != nil {
				return fmt.Errorf("telegram.daily_report_time must use HH:MM format: %w", err)
			}
		}
	}
	return nil
}

// Manager 提供配置的并发安全读取与 SIGHUP 热重载。
type Manager struct {
	mu      sync.RWMutex
	current Config
	path    string
}

// NewManager 创建管理器并完成首次加载，失败即报错退出。
func NewManager(path string) (*Manager, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	return &Manager{current: cfg, path: path}, nil
}

// Current 返回当前配置快照。
func (m *Manager) Current() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

// Reload 重新加载配置；失败时保留旧配置继续服务。
func (m *Manager) Reload() error {
	cfg, err := Load(m.path)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.current = cfg
	m.mu.Unlock()
	return nil
}
