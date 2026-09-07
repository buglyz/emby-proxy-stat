package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	passwordHashAlgorithm  = "pbkdf2-sha256"
	passwordHashIterations = 210000
	loginWindow            = 10 * time.Minute
	loginBlockDuration     = 15 * time.Minute
	loginMaxFailures       = 5
)

type loginFailureState struct {
	windowStart  time.Time
	failures     int
	blockedUntil time.Time
}

var (
	loginMu       sync.Mutex
	loginFailures = make(map[string]loginFailureState)
)

func validateConfig(cfg Config) error {
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
		if _, _, _, err := parsePasswordHash(cfg.Auth.PasswordHash); err != nil {
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

func verifyPassword(candidate string, cfg Config) bool {
	if cfg.Auth.PasswordHash != "" {
		iterations, salt, expected, err := parsePasswordHash(cfg.Auth.PasswordHash)
		if err != nil {
			return false
		}
		actual := pbkdf2SHA256([]byte(candidate), salt, iterations, len(expected))
		return subtle.ConstantTimeCompare(actual, expected) == 1
	}
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(cfg.Auth.Password)) == 1
}

func parsePasswordHash(encoded string) (int, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != passwordHashAlgorithm {
		return 0, nil, nil, fmt.Errorf("unsupported password hash format")
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 100000 || iterations > 1000000 {
		return 0, nil, nil, fmt.Errorf("invalid PBKDF2 iteration count")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) < 16 {
		return 0, nil, nil, fmt.Errorf("invalid PBKDF2 salt")
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(expected) < sha256.Size {
		return 0, nil, nil, fmt.Errorf("invalid PBKDF2 digest")
	}
	return iterations, salt, expected, nil
}

func pbkdf2SHA256(password, salt []byte, iterations, keyLength int) []byte {
	result := make([]byte, keyLength)
	blockCount := (keyLength + sha256.Size - 1) / sha256.Size
	for block := 1; block <= blockCount; block++ {
		mac := hmac.New(sha256.New, password)
		_, _ = mac.Write(salt)
		var blockIndex [4]byte
		binary.BigEndian.PutUint32(blockIndex[:], uint32(block))
		_, _ = mac.Write(blockIndex[:])
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			mac = hmac.New(sha256.New, password)
			_, _ = mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		start := (block - 1) * sha256.Size
		copy(result[start:], t)
	}
	return result
}

func loginClientKey(r *http.Request) string {
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
		if ip := net.ParseIP(forwarded); ip != nil {
			return ip.String()
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if ip := net.ParseIP(host); ip != nil {
			return ip.String()
		}
	}
	if ip := net.ParseIP(r.RemoteAddr); ip != nil {
		return ip.String()
	}
	return "unknown"
}

func loginRetryAfter(key string) int {
	now := time.Now()
	loginMu.Lock()
	defer loginMu.Unlock()
	state, ok := loginFailures[key]
	if !ok {
		return 0
	}
	if now.Before(state.blockedUntil) {
		return int(time.Until(state.blockedUntil).Seconds()) + 1
	}
	if now.Sub(state.windowStart) >= loginWindow {
		delete(loginFailures, key)
	}
	return 0
}

func recordLoginFailure(key string) {
	now := time.Now()
	loginMu.Lock()
	defer loginMu.Unlock()
	state := loginFailures[key]
	if state.windowStart.IsZero() || now.Sub(state.windowStart) >= loginWindow {
		state = loginFailureState{windowStart: now}
	}
	state.failures++
	if state.failures >= loginMaxFailures {
		state.blockedUntil = now.Add(loginBlockDuration)
	}
	loginFailures[key] = state
	if len(loginFailures) > 4096 {
		for candidate, value := range loginFailures {
			if now.Sub(value.windowStart) >= loginWindow && now.After(value.blockedUntil) {
				delete(loginFailures, candidate)
			}
		}
	}
}

func clearLoginFailures(key string) {
	loginMu.Lock()
	delete(loginFailures, key)
	loginMu.Unlock()
}

func generatePasswordHash(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	digest := pbkdf2SHA256([]byte(password), salt, passwordHashIterations, sha256.Size)
	return fmt.Sprintf("%s$%d$%s$%s", passwordHashAlgorithm, passwordHashIterations,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(digest)), nil
}
