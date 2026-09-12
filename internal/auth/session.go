package auth

import (
	"sync"
	"time"
)

// SessionManager 是基于内存 map 的过期会话存储。
// 服务重启后会话失效，用户需要重新登录（与既有行为一致）。
type SessionManager struct {
	mu       sync.Mutex
	sessions map[string]time.Time // token -> 过期时间
	ttl      time.Duration
}

func NewSessionManager(ttl time.Duration) *SessionManager {
	return &SessionManager{
		sessions: make(map[string]time.Time),
		ttl:      ttl,
	}
}

// Create 签发新会话，返回令牌与过期时间。
func (m *SessionManager) Create() (token string, expires time.Time, err error) {
	token, err = GenerateToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expires = time.Now().Add(m.ttl)
	m.mu.Lock()
	m.sessions[token] = expires
	m.mu.Unlock()
	return token, expires, nil
}

// Valid 校验令牌；顺手清除已过期项，避免长期占用内存。
func (m *SessionManager) Valid(token string) bool {
	if token == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	expires, ok := m.sessions[token]
	if !ok || time.Now().After(expires) {
		delete(m.sessions, token)
		return false
	}
	return true
}

// Destroy 作废令牌（登出）。
func (m *SessionManager) Destroy(token string) {
	m.mu.Lock()
	delete(m.sessions, token)
	m.mu.Unlock()
}

// Prune 清除全部过期会话，由后台周期任务调用。
func (m *SessionManager) Prune() {
	now := time.Now()
	m.mu.Lock()
	for token, expires := range m.sessions {
		if now.After(expires) {
			delete(m.sessions, token)
		}
	}
	m.mu.Unlock()
}
