package store

import (
	"testing"
	"time"
)

// 回归：滑动防抖——持续观看同一内容（心跳间隔 < 窗口）只计一次播放；
// 停止超过窗口后再观看才计新的一次。修复长视频每 30 分钟被重复计数的问题。
func TestRecordPlaySlidingDebounce(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2026, 9, 12, 20, 0, 0, 0, time.UTC)
	record := func(offset time.Duration) {
		err := s.RecordPlay(PlayEvent{
			ClientIP: "1.1.1.1", TargetHost: "up.example", ItemID: "long-movie",
			LogTime: base.Add(offset),
		})
		if err != nil {
			t.Fatalf("record at +%v: %v", offset, err)
		}
	}

	// 120 分钟连续观看，每 10 分钟一次心跳
	for m := 0; m <= 120; m += 10 {
		record(time.Duration(m) * time.Minute)
	}
	stats, err := s.Stats(base.Format("2006-01-02"))
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.TotalPlays != 1 {
		t.Fatalf("continuous watch must count once, got %d", stats.TotalPlays)
	}

	// 停止 31 分钟后再次观看 → 新的一次
	record(151 * time.Minute)
	stats, _ = s.Stats(base.Format("2006-01-02"))
	if stats.TotalPlays != 2 {
		t.Fatalf("resume after gap must count again, got %d", stats.TotalPlays)
	}
}

// 流量明细：落库后分布查询聚合正确（按客户端与按节点）。
func TestBreakdown(t *testing.T) {
	s := newTestStore(t)
	today := time.Now().Format("2006-01-02")

	s.AddTraffic(today, "1.1.1.1", "node-a", 1000)
	s.AddTraffic(today, "1.1.1.1", "node-b", 500)
	s.AddTraffic(today, "2.2.2.2", "node-a", 300)
	if err := s.FlushTraffic(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	breakdown, err := s.Breakdown(today)
	if err != nil {
		t.Fatalf("breakdown: %v", err)
	}
	if len(breakdown.ByClient) != 2 || len(breakdown.ByHost) != 2 {
		t.Fatalf("unexpected group counts: %+v", breakdown)
	}
	// 按字节降序：客户端 1.1.1.1 (1500) 在前
	if breakdown.ByClient[0].Key != "1.1.1.1" || breakdown.ByClient[0].Bytes != 1500 {
		t.Fatalf("unexpected top client: %+v", breakdown.ByClient[0])
	}
	if breakdown.ByHost[0].Key != "node-a" || breakdown.ByHost[0].Bytes != 1300 {
		t.Fatalf("unexpected top host: %+v", breakdown.ByHost[0])
	}
	if breakdown.ByClient[0].BytesFmt == "" {
		t.Fatal("bytes_fmt must be populated")
	}
}

// 未落库的明细缓冲也要计入分布，且与 Stats 的总量口径一致。
func TestBreakdownIncludesBufferedTraffic(t *testing.T) {
	s := newTestStore(t)
	today := time.Now().Format("2006-01-02")

	s.AddTraffic(today, "3.3.3.3", "node-c", 777)
	breakdown, err := s.Breakdown(today)
	if err != nil {
		t.Fatalf("breakdown: %v", err)
	}
	if len(breakdown.ByClient) != 1 || breakdown.ByClient[0].Bytes != 777 {
		t.Fatalf("buffered detail missing: %+v", breakdown.ByClient)
	}
	stats, err := s.Stats(today)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.TodayBytes != 777 {
		t.Fatalf("global stats mismatch: %+v", stats)
	}
}

// 趋势：日期连续、无数据日为零值。
func TestTrend(t *testing.T) {
	s := newTestStore(t)
	today := time.Now()
	todayStr := today.Format("2006-01-02")
	yesterdayStr := today.AddDate(0, 0, -1).Format("2006-01-02")

	s.AddTraffic(todayStr, "1.1.1.1", "node-a", 2000)
	if err := s.FlushTraffic(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	_ = s.RecordPlay(PlayEvent{ClientIP: "1.1.1.1", TargetHost: "node-a", ItemID: "x", LogTime: today})
	_ = s.RecordPlay(PlayEvent{ClientIP: "1.1.1.1", TargetHost: "node-a", ItemID: "y", LogTime: today.Add(time.Minute)})

	points, err := s.Trend(7)
	if err != nil {
		t.Fatalf("trend: %v", err)
	}
	if len(points) != 7 {
		t.Fatalf("expected 7 points, got %d", len(points))
	}
	// 升序：最后一天是今天
	last := points[len(points)-1]
	if last.Date != todayStr || last.Plays != 2 || last.Bytes != 2000 {
		t.Fatalf("unexpected today point: %+v", last)
	}
	// 昨天没有播放，但日期点必须存在
	found := false
	for _, p := range points {
		if p.Date == yesterdayStr {
			found = true
			if p.Plays != 0 {
				t.Fatalf("yesterday should have zero plays, got %d", p.Plays)
			}
		}
	}
	if !found {
		t.Fatal("yesterday point missing")
	}
}

// 保留策略：早于截止日期的记录被删除，边界内的保留。
func TestPruneOld(t *testing.T) {
	s := newTestStore(t)
	old := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	_ = s.RecordPlay(PlayEvent{ClientIP: "1.1.1.1", TargetHost: "n", ItemID: "a", LogTime: old})
	_ = s.RecordPlay(PlayEvent{ClientIP: "1.1.1.1", TargetHost: "n", ItemID: "b", LogTime: newer})
	s.AddTraffic(old.Format("2006-01-02"), "1.1.1.1", "n", 100)
	s.AddTraffic(newer.Format("2006-01-02"), "1.1.1.1", "n", 200)
	if err := s.FlushTraffic(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if err := s.PruneOld("2026-06-01"); err != nil {
		t.Fatalf("prune: %v", err)
	}

	stats, err := s.Stats(newer.Format("2006-01-02"))
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.TotalPlays != 1 || stats.TotalBytes != 200 {
		t.Fatalf("old records must be pruned: plays=%d bytes=%d", stats.TotalPlays, stats.TotalBytes)
	}
}
