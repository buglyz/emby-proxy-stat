package caddylog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"emby-proxy-stat/internal/store"
)

// fakeRecorder 捕获流水线产出，避免真实数据库依赖。
type fakeRecorder struct {
	plays   []store.PlayEvent
	traffic map[string]int64
}

func (f *fakeRecorder) AddTraffic(date, clientIP, targetHost string, sizeBytes int64) {
	f.traffic[date] += sizeBytes
}

func (f *fakeRecorder) RecordPlay(ev store.PlayEvent) error {
	f.plays = append(f.plays, ev)
	return nil
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
}

// 端到端：媒体流请求 + 随后心跳 → 记录真实 ItemID 与设备名。
func TestTailPipelineBindsStreamToHeartbeat(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "caddy.log")
	rec := &fakeRecorder{traffic: map[string]int64{}}
	tl := NewTailer(rec, logPath)
	tl.started = true // 模拟已运行中遇轮转重开：从头读取全部历史行

	ts := float64(time.Now().Unix())
	stream := fmt.Sprintf(`{"ts":%f,"request":{"client_ip":"9.9.9.9","remote_ip":"9.9.9.9","uri":"/https://up.example:443/Videos/item77/stream.mp4","headers":{"X-Emby-Authorization":["MediaBrowser Client=\"Infuse\", Device=\"iPad\", DeviceId=\"d1\", Version=\"1\""]}},"status":200,"size":64000}`, ts)
	progress := fmt.Sprintf(`{"ts":%f,"request":{"client_ip":"9.9.9.9","remote_ip":"9.9.9.9","uri":"/https://up.example:443/Sessions/Playing/Progress","headers":{}},"status":204,"size":0}`, ts)
	mustWrite(t, logPath, stream+"\n"+progress+"\n")

	// followOnce 会持续轮询跟踪文件，用超时 ctx 在读完已有行后退出
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := tl.followOnce(ctx); err != nil {
		t.Fatalf("followOnce: %v", err)
	}

	if len(rec.plays) != 1 {
		t.Fatalf("expected 1 play event, got %d: %+v", len(rec.plays), rec.plays)
	}
	play := rec.plays[0]
	if play.ItemID != "item77" {
		t.Fatalf("expected stream-bound item id, got %q", play.ItemID)
	}
	if play.ClientIP != "9.9.9.9" || play.TargetHost != "up.example:443" {
		t.Fatalf("unexpected identity: %+v", play)
	}
	if play.DeviceName != "iPad (Infuse)" {
		t.Fatalf("expected auth-header device, got %q", play.DeviceName)
	}
	if rec.traffic[time.Now().Format("2006-01-02")] != 64000 {
		t.Fatalf("traffic not accumulated: %v", rec.traffic)
	}
}

// 回归：没有媒体流上下文的心跳按 session 级记录，不虚报 ItemID。
func TestTailPipelineSessionFallback(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "caddy.log")
	rec := &fakeRecorder{traffic: map[string]int64{}}
	tl := NewTailer(rec, logPath)
	tl.started = true // 模拟已运行中遇轮转重开：从头读取全部历史行

	ts := float64(time.Now().Unix())
	progress := fmt.Sprintf(`{"ts":%f,"request":{"client_ip":"8.8.8.8","remote_ip":"8.8.8.8","uri":"/https://up.example/Sessions/Playing/Progress","headers":{}},"status":204,"size":0}`, ts)
	mustWrite(t, logPath, progress+"\n")

	// followOnce 会持续轮询跟踪文件，用超时 ctx 在读完已有行后退出
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := tl.followOnce(ctx); err != nil {
		t.Fatalf("followOnce: %v", err)
	}
	if len(rec.plays) != 1 || rec.plays[0].ItemID != "session" {
		t.Fatalf("expected session-level play, got %+v", rec.plays)
	}
}

// 回归：错误状态与仪表盘自身请求不产生统计。
func TestTailPipelineFiltersNoise(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "caddy.log")
	rec := &fakeRecorder{traffic: map[string]int64{}}
	tl := NewTailer(rec, logPath)
	tl.started = true // 模拟已运行中遇轮转重开：从头读取全部历史行

	ts := float64(time.Now().Unix())
	lines := fmt.Sprintf(`{"ts":%f,"request":{"client_ip":"1.1.1.1","uri":"/https://up.example/Videos/x/stream","headers":{}},"status":404,"size":100}`, ts) + "\n" +
		fmt.Sprintf(`{"ts":%f,"request":{"client_ip":"1.1.1.1","uri":"/api/stats","headers":{}},"status":200,"size":500}`, ts) + "\n" +
		"caddy startup noise\n"
	mustWrite(t, logPath, lines)

	// followOnce 会持续轮询跟踪文件，用超时 ctx 在读完已有行后退出
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := tl.followOnce(ctx); err != nil {
		t.Fatalf("followOnce: %v", err)
	}
	if len(rec.plays) != 0 || len(rec.traffic) != 0 {
		t.Fatalf("noise must not be recorded: plays=%v traffic=%v", rec.plays, rec.traffic)
	}
}

// 回归：详情页预加载（PlaybackInfo）不算播放。
func TestTailPipelineIgnoresPlaybackInfo(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "caddy.log")
	rec := &fakeRecorder{traffic: map[string]int64{}}
	tl := NewTailer(rec, logPath)
	tl.started = true // 模拟已运行中遇轮转重开：从头读取全部历史行

	ts := float64(time.Now().Unix())
	line := fmt.Sprintf(`{"ts":%f,"request":{"client_ip":"1.1.1.1","uri":"/emby/Items/abc123/PlaybackInfo","headers":{}},"status":200,"size":4096}`, ts)
	mustWrite(t, logPath, line+"\n")

	// followOnce 会持续轮询跟踪文件，用超时 ctx 在读完已有行后退出
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := tl.followOnce(ctx); err != nil {
		t.Fatalf("followOnce: %v", err)
	}
	if len(rec.plays) != 0 {
		t.Fatalf("PlaybackInfo must not count as play: %+v", rec.plays)
	}
}
