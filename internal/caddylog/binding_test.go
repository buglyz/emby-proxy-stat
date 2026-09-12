package caddylog

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// 回归：移动 CGNAT 下客户端 IP 漂移，设备键仍能把心跳绑定到媒体流。
func TestTailPipelineBindsAcrossIPDrift(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "caddy.log")
	rec := &fakeRecorder{traffic: map[string]int64{}}
	tl := NewTailer(rec, logPath)
	tl.started = true

	ts := float64(time.Now().Unix())
	authHeader := `{"X-Emby-Authorization":["MediaBrowser Client=\"Infuse\", Device=\"iPhone 15\", DeviceId=\"d1\", Version=\"7\""]}`
	stream := fmt.Sprintf(`{"ts":%f,"request":{"client_ip":"10.10.10.1","remote_ip":"10.10.10.1","uri":"/https://up.example/Videos/drama42/stream.mp4","headers":%s},"status":200,"size":50000}`, ts, authHeader)
	progress := fmt.Sprintf(`{"ts":%f,"request":{"client_ip":"10.10.10.99","remote_ip":"10.10.10.99","uri":"/https://up.example/Sessions/Playing/Progress","headers":%s},"status":204,"size":0}`, ts, authHeader)
	mustWrite(t, logPath, stream+"\n"+progress+"\n")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := tl.followOnce(ctx); err != nil {
		t.Fatalf("followOnce: %v", err)
	}
	if len(rec.plays) != 1 || rec.plays[0].ItemID != "drama42" {
		t.Fatalf("NAT drift must still bind stream, got %+v", rec.plays)
	}
}

// 无设备名的心跳走 IP 键兜底。
func TestTailPipelineIPKeyFallback(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "caddy.log")
	rec := &fakeRecorder{traffic: map[string]int64{}}
	tl := NewTailer(rec, logPath)
	tl.started = true

	ts := float64(time.Now().Unix())
	stream := fmt.Sprintf(`{"ts":%f,"request":{"client_ip":"7.7.7.7","remote_ip":"7.7.7.7","uri":"/https://up.example/Videos/movie9/stream","headers":{}},"status":200,"size":1000}`, ts)
	progress := fmt.Sprintf(`{"ts":%f,"request":{"client_ip":"7.7.7.7","remote_ip":"7.7.7.7","uri":"/https://up.example/Sessions/Playing/Progress","headers":{}},"status":204,"size":0}`, ts)
	mustWrite(t, logPath, stream+"\n"+progress+"\n")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := tl.followOnce(ctx); err != nil {
		t.Fatalf("followOnce: %v", err)
	}
	if len(rec.plays) != 1 || rec.plays[0].ItemID != "movie9" {
		t.Fatalf("heartbeat without device must fall back to IP key, got %+v", rec.plays)
	}
}

// 实时会话快照：活跃会话 Active=true。
func TestActiveSessions(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "caddy.log")
	rec := &fakeRecorder{traffic: map[string]int64{}}
	tl := NewTailer(rec, logPath)
	tl.started = true

	ts := float64(time.Now().Unix())
	stream := fmt.Sprintf(`{"ts":%f,"request":{"client_ip":"6.6.6.6","remote_ip":"6.6.6.6","uri":"/https://node.example:8096/Videos/live1/stream","headers":{}},"status":200,"size":100}`, ts)
	mustWrite(t, logPath, stream+"\n")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := tl.followOnce(ctx); err != nil {
		t.Fatalf("followOnce: %v", err)
	}

	sessions := tl.ActiveSessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	s := sessions[0]
	if !s.Active || s.ItemID != "live1" || s.TargetHost != "node.example:8096" {
		t.Fatalf("unexpected session snapshot: %+v", s)
	}
}
