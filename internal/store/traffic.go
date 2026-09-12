package store

import (
	"database/sql"
	"fmt"
	"time"
)

// AddTraffic 在内存缓冲中累加某日流量（热路径，写内存不落盘）。
// 同时记录全局总量与 客户端×节点 明细，二者由 FlushTraffic 同一事务落库。
func (s *Store) AddTraffic(date, clientIP, targetHost string, sizeBytes int64) {
	s.trafficMu.Lock()
	defer s.trafficMu.Unlock()
	entry, ok := s.traffic[date]
	if !ok {
		entry = &TrafficEntry{}
		s.traffic[date] = entry
	}
	entry.Bytes += sizeBytes
	entry.Requests++

	dk := trafficDetailKey{date: date, client: clientIP, target: targetHost}
	detail, ok := s.trafficDetail[dk]
	if !ok {
		detail = &TrafficEntry{}
		s.trafficDetail[dk] = detail
	}
	detail.Bytes += sizeBytes
	detail.Requests++
}

// FlushTraffic 将内存缓冲（全局 + 明细）批量落库；失败时把快照放回缓冲等待下次重试。
// 与 Stats/RecordPlay 共享数据库锁，保证统计口径一致。
func (s *Store) FlushTraffic() error {
	s.trafficMu.Lock()
	if len(s.traffic) == 0 && len(s.trafficDetail) == 0 {
		s.trafficMu.Unlock()
		return nil
	}
	batch := make(map[string]TrafficEntry, len(s.traffic))
	for date, entry := range s.traffic {
		batch[date] = *entry
	}
	detailBatch := make(map[trafficDetailKey]TrafficEntry, len(s.trafficDetail))
	for key, entry := range s.trafficDetail {
		detailBatch[key] = *entry
	}
	s.traffic = make(map[string]*TrafficEntry)
	s.trafficDetail = make(map[trafficDetailKey]*TrafficEntry)
	s.trafficMu.Unlock()

	if err := s.persistTrafficBatch(batch, detailBatch); err != nil {
		s.restoreTrafficBatch(batch, detailBatch)
		return err
	}
	return nil
}

// FlushTrafficOnce 是进程退出前的最终冲刷，尽力而为。
func (s *Store) FlushTrafficOnce() {
	if err := s.FlushTraffic(); err != nil {
		// 退出路径没有重试机会，仅记录
		fmt.Printf("[Shutdown] flush traffic failed: %v\n", err)
	}
}

func (s *Store) persistTrafficBatch(batch map[string]TrafficEntry, detailBatch map[trafficDetailKey]TrafficEntry) error {
	return s.withDB(func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin transaction: %w", err)
		}
		commit := func(err error) error {
			_ = tx.Rollback()
			return err
		}

		globalStmt, err := tx.Prepare(`
			INSERT INTO daily_traffic (date, bytes, requests, updated_at)
			VALUES (?, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(date) DO UPDATE SET
				bytes = bytes + excluded.bytes,
				requests = requests + excluded.requests,
				updated_at = CURRENT_TIMESTAMP
		`)
		if err != nil {
			return commit(fmt.Errorf("prepare daily_traffic statement: %w", err))
		}
		for date, entry := range batch {
			if _, err := globalStmt.Exec(date, entry.Bytes, entry.Requests); err != nil {
				_ = globalStmt.Close()
				return commit(fmt.Errorf("write daily_traffic for %s: %w", date, err))
			}
		}
		if err := globalStmt.Close(); err != nil {
			return commit(fmt.Errorf("close daily_traffic statement: %w", err))
		}

		detailStmt, err := tx.Prepare(`
			INSERT INTO traffic_by_client (date, client_ip, target_host, bytes, requests, updated_at)
			VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(date, client_ip, target_host) DO UPDATE SET
				bytes = bytes + excluded.bytes,
				requests = requests + excluded.requests,
				updated_at = CURRENT_TIMESTAMP
		`)
		if err != nil {
			return commit(fmt.Errorf("prepare traffic_by_client statement: %w", err))
		}
		for key, entry := range detailBatch {
			if _, err := detailStmt.Exec(key.date, key.client, key.target, entry.Bytes, entry.Requests); err != nil {
				_ = detailStmt.Close()
				return commit(fmt.Errorf("write traffic_by_client for %v: %w", key, err))
			}
		}
		if err := detailStmt.Close(); err != nil {
			return commit(fmt.Errorf("close traffic_by_client statement: %w", err))
		}

		if err := tx.Commit(); err != nil {
			return commit(fmt.Errorf("commit traffic batch: %w", err))
		}
		return nil
	})
}

func (s *Store) restoreTrafficBatch(batch map[string]TrafficEntry, detailBatch map[trafficDetailKey]TrafficEntry) {
	s.trafficMu.Lock()
	defer s.trafficMu.Unlock()
	for date, entry := range batch {
		current := s.traffic[date]
		if current == nil {
			current = &TrafficEntry{}
			s.traffic[date] = current
		}
		current.Bytes += entry.Bytes
		current.Requests += entry.Requests
	}
	for key, entry := range detailBatch {
		current := s.trafficDetail[key]
		if current == nil {
			current = &TrafficEntry{}
			s.trafficDetail[key] = current
		}
		current.Bytes += entry.Bytes
		current.Requests += entry.Requests
	}
}

// PlayEvent 是一次有效播放。
type PlayEvent struct {
	ClientIP   string
	TargetHost string
	ItemID     string
	URI        string
	DeviceName string
	LogTime    time.Time
}

// claimDebounce 原子占位并采用滑动窗口：窗口内的心跳会刷新时间戳，
// 因此持续观看同一内容只计一次播放；停止超过窗口后再观看才计新的一次。
// 先占位后落库，落库失败回滚占位，避免既丢数据又卡去重窗口。
func (s *Store) claimDebounce(key string, at time.Time) bool {
	now := at.Unix()
	window := int64(s.debounceWindow.Seconds())
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	for k, v := range s.recentPlays {
		if now-v > window {
			delete(s.recentPlays, k)
		}
	}
	if last, ok := s.recentPlays[key]; ok && now-last < window {
		s.recentPlays[key] = now // 滑动续期
		return false
	}
	s.recentPlays[key] = now
	return true
}

func (s *Store) releaseDebounce(key string) {
	s.recentMu.Lock()
	delete(s.recentPlays, key)
	s.recentMu.Unlock()
}

// RecordPlay 记录一次有效播放，窗口内重复播放自动去重。
func (s *Store) RecordPlay(ev PlayEvent) error {
	dedupKey := ev.ClientIP + ":" + ev.TargetHost + ":" + ev.ItemID
	if !s.claimDebounce(dedupKey, ev.LogTime) {
		return nil
	}
	err := s.withDB(func(db *sql.DB) error {
		_, e := db.Exec(`
			INSERT INTO play_events (played_at, play_date, client_ip, target_host, item_id, uri, device_name)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, ev.LogTime.Format("2006-01-02 15:04:05"), ev.LogTime.Format("2006-01-02"),
			ev.ClientIP, ev.TargetHost, ev.ItemID, ev.URI, ev.DeviceName)
		return e
	})
	if err != nil {
		s.releaseDebounce(dedupKey)
		return err
	}
	return nil
}

// PruneOld 删除早于截止日期的播放与流量记录（数据保留策略）。
// cutoff 为 YYYY-MM-DD 字符串，早于该日期的记录被删除。
func (s *Store) PruneOld(cutoff string) error {
	return s.withDB(func(db *sql.DB) error {
		if _, err := db.Exec("DELETE FROM play_events WHERE play_date < ?", cutoff); err != nil {
			return fmt.Errorf("prune play_events: %w", err)
		}
		if _, err := db.Exec("DELETE FROM daily_traffic WHERE date < ?", cutoff); err != nil {
			return fmt.Errorf("prune daily_traffic: %w", err)
		}
		if _, err := db.Exec("DELETE FROM traffic_by_client WHERE date < ?", cutoff); err != nil {
			return fmt.Errorf("prune traffic_by_client: %w", err)
		}
		return nil
	})
}
