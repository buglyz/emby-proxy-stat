package auth

import (
	"sync"
	"time"
)

// 登录限流默认参数：10 分钟窗口内失败 5 次即封禁 15 分钟。
const (
	LoginWindow      = 10 * time.Minute
	LoginBlock       = 15 * time.Minute
	LoginMaxFailures = 5
	loginMapLimit    = 4096
)

type loginFailureState struct {
	windowStart  time.Time
	failures     int
	blockedUntil time.Time
}

// LoginLimiter 按客户端 IP 限制登录尝试频率，防止在线爆破。
type LoginLimiter struct {
	mu       sync.Mutex
	failures map[string]loginFailureState
	window   time.Duration
	block    time.Duration
	max      int
}

func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{
		failures: make(map[string]loginFailureState),
		window:   LoginWindow,
		block:    LoginBlock,
		max:      LoginMaxFailures,
	}
}

// RetryAfter 返回该客户端仍需等待的秒数；0 表示允许尝试。
func (l *LoginLimiter) RetryAfter(key string) int {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	state, ok := l.failures[key]
	if !ok {
		return 0
	}
	if now.Before(state.blockedUntil) {
		return int(time.Until(state.blockedUntil).Seconds()) + 1
	}
	if now.Sub(state.windowStart) >= l.window {
		delete(l.failures, key)
	}
	return 0
}

// RecordFailure 记录一次失败；达到阈值后进入封禁。
func (l *LoginLimiter) RecordFailure(key string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.failures[key]
	if state.windowStart.IsZero() || now.Sub(state.windowStart) >= l.window {
		state = loginFailureState{windowStart: now}
	}
	state.failures++
	if state.failures >= l.max {
		state.blockedUntil = now.Add(l.block)
	}
	l.failures[key] = state
	if len(l.failures) > loginMapLimit {
		for candidate, value := range l.failures {
			if now.Sub(value.windowStart) >= l.window && now.After(value.blockedUntil) {
				delete(l.failures, candidate)
			}
		}
	}
}

// Reset 登录成功后清除该客户端的失败记录。
func (l *LoginLimiter) Reset(key string) {
	l.mu.Lock()
	delete(l.failures, key)
	l.mu.Unlock()
}
