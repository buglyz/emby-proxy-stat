package auth

import (
	"testing"
	"time"
)

func TestLoginLimiterBlocksAfterMaxFailures(t *testing.T) {
	l := NewLoginLimiter()
	const key = "1.2.3.4"

	for i := 0; i < LoginMaxFailures; i++ {
		if retry := l.RetryAfter(key); retry != 0 {
			t.Fatalf("attempt %d should be allowed, retry after %ds", i+1, retry)
		}
		l.RecordFailure(key)
	}
	if retry := l.RetryAfter(key); retry <= 0 {
		t.Fatal("expected client to be blocked after max failures")
	}
}

func TestLoginLimiterReset(t *testing.T) {
	l := NewLoginLimiter()
	const key = "5.6.7.8"
	for i := 0; i < LoginMaxFailures; i++ {
		l.RecordFailure(key)
	}
	l.Reset(key)
	if retry := l.RetryAfter(key); retry != 0 {
		t.Fatalf("expected reset to clear block, retry after %ds", retry)
	}
}

func TestLoginLimiterKeysAreIsolated(t *testing.T) {
	l := NewLoginLimiter()
	for i := 0; i < LoginMaxFailures; i++ {
		l.RecordFailure("blocked-client")
	}
	if retry := l.RetryAfter("other-client"); retry != 0 {
		t.Fatalf("unrelated client must not be blocked, got %ds", retry)
	}
}

func TestSessionManagerLifecycle(t *testing.T) {
	m := NewSessionManager(time.Hour)

	token, expires, err := m.Create()
	if err != nil || token == "" || expires.IsZero() {
		t.Fatalf("create session: token=%q err=%v", token, err)
	}
	if !m.Valid(token) {
		t.Fatal("fresh session must be valid")
	}
	m.Destroy(token)
	if m.Valid(token) {
		t.Fatal("destroyed session must be invalid")
	}
	if m.Valid("") || m.Valid("nonexistent") {
		t.Fatal("empty/unknown tokens must be invalid")
	}
}

func TestSessionManagerExpiry(t *testing.T) {
	m := NewSessionManager(-time.Minute) // 立即过期
	token, _, err := m.Create()
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if m.Valid(token) {
		t.Fatal("expired session must be invalid")
	}
}
