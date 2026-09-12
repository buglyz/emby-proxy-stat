package store

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRecordPlayDebounce(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	ev := PlayEvent{ClientIP: "1.1.1.1", TargetHost: "up.example", ItemID: "m1", URI: "/u", LogTime: now}

	if err := s.RecordPlay(ev); err != nil {
		t.Fatalf("first record: %v", err)
	}
	if err := s.RecordPlay(ev); err != nil {
		t.Fatalf("duplicate record: %v", err)
	}
	stats, err := s.Stats(now.Format("2006-01-02"))
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.TotalPlays != 1 {
		t.Fatalf("expected 1 play after debounce, got %d", stats.TotalPlays)
	}
}

// 回归：落库失败必须回滚防抖占位，否则该播放会被窗口静默丢弃。
func TestRecordPlayRollsBackDebounceOnDBError(t *testing.T) {
	s := newTestStore(t)
	s.Close() // 关库制造落库失败
	now := time.Now()
	ev := PlayEvent{ClientIP: "2.2.2.2", TargetHost: "up.example", ItemID: "m2", LogTime: now}

	if err := s.RecordPlay(ev); err == nil {
		t.Fatal("expected insert error on closed db")
	}
	// 重新打开同一路径验证占位已回滚（用内存中的 recentPlays 判断）
	s.recentMu.Lock()
	_, claimed := s.recentPlays["2.2.2.2:up.example:m2"]
	s.recentMu.Unlock()
	if claimed {
		t.Fatal("failed insert must release the debounce claim")
	}
}

func TestTrafficFlushAndStatsConsistency(t *testing.T) {
	s := newTestStore(t)
	today := time.Now().Format("2006-01-02")

	s.AddTraffic(today, "1.1.1.1", "node-a", 1500)
	s.AddTraffic(today, "1.1.1.1", "node-a", 500)

	stats, err := s.Stats(today)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.TodayBytes != 2000 || stats.TotalBytes != 2000 {
		t.Fatalf("pre-flush stats mismatch: %+v", stats)
	}
	if err := s.FlushTraffic(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// 回归：刷盘后再次统计，已落库字节不得与缓冲重复计入
	stats, err = s.Stats(today)
	if err != nil {
		t.Fatalf("stats after flush: %v", err)
	}
	if stats.TodayBytes != 2000 || stats.TotalBytes != 2000 {
		t.Fatalf("post-flush stats double-counted: %+v", stats)
	}
}

func TestFlushRestoresBatchOnError(t *testing.T) {
	s := newTestStore(t)
	today := time.Now().Format("2006-01-02")
	s.AddTraffic(today, "2.2.2.2", "node-b", 999)
	_ = s.Close()

	if err := s.FlushTraffic(); err == nil {
		t.Fatal("expected flush error on closed db")
	}
	s.trafficMu.Lock()
	entry, ok := s.traffic[today]
	s.trafficMu.Unlock()
	if !ok || entry.Bytes != 999 {
		t.Fatal("failed batch must be restored into the buffer")
	}
}

func TestTodayClientsAggregation(t *testing.T) {
	s := newTestStore(t)
	today := time.Now()
	base := PlayEvent{ClientIP: "3.3.3.3", TargetHost: "node-a", ItemID: "x", DeviceName: "iPhone", LogTime: today}
	_ = s.RecordPlay(base)
	// 同设备不同 ItemID → 聚合为一条、计数 2（同 ItemID 会被防抖去重）
	_ = s.RecordPlay(PlayEvent{ClientIP: "3.3.3.3", TargetHost: "node-a", ItemID: "y", DeviceName: "iPhone", LogTime: today.Add(time.Minute)})
	// 无设备名 → 单独聚合并回退为 未知设备
	_ = s.RecordPlay(PlayEvent{ClientIP: "4.4.4.4", TargetHost: "node-b", ItemID: "z", DeviceName: "", LogTime: today.Add(2 * time.Minute)})

	resp, err := s.TodayClients(today.Format("2006-01-02"))
	if err != nil {
		t.Fatalf("today clients: %v", err)
	}
	if resp.Total != 2 || len(resp.Clients) != 2 {
		t.Fatalf("expected 2 aggregated clients, got %+v", resp)
	}
	first := resp.Clients[0]
	if first.ClientIP != "3.3.3.3" || first.TargetHost != "node-a" || first.PlayCount != 2 || first.DeviceName != "iPhone" {
		t.Fatalf("unexpected first aggregate: %+v", first)
	}
	if resp.Clients[1].DeviceName != "未知设备" {
		t.Fatalf("empty device must fall back, got %q", resp.Clients[1].DeviceName)
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[int64]string{
		0:                      "0 B",
		512:                    "512 B",
		2048:                   "2.00 KB",
		5 * 1024 * 1024:        "5.00 MB",
		3 * 1024 * 1024 * 1024: "3.00 GB",
	}
	for input, want := range cases {
		if got := FormatBytes(input); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", input, got, want)
		}
	}
}
