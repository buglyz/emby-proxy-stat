package store

import (
	"database/sql"
	"fmt"
	"sort"

	"emby-proxy-stat/internal/clock"
)

// StatsResponse 是仪表盘统计接口的完整载荷。
// 字段名与前端契约绑定，不得改名。
type StatsResponse struct {
	TodayPlays      int64  `json:"today_plays"`
	TotalPlays      int64  `json:"total_plays"`
	TodayBytes      int64  `json:"today_bytes"`
	TotalBytes      int64  `json:"total_bytes"`
	TodayTrafficFmt string `json:"today_traffic_fmt"`
	TotalTrafficFmt string `json:"total_traffic_fmt"`
	TodayClients    int64  `json:"today_clients"`
	TotalClients    int64  `json:"total_clients"`
	TodayHosts      int64  `json:"today_hosts"`
	TotalHosts      int64  `json:"total_hosts"`
	Date            string `json:"date"`
	Status          string `json:"status"`
}

// ClientInfo 是单个 客户端IP×上游节点×设备 组合的今日播放聚合。
type ClientInfo struct {
	ClientIP   string `json:"client_ip"`
	DeviceName string `json:"device_name"`
	TargetHost string `json:"target_host"`
	PlayCount  int64  `json:"play_count"`
	LastPlayed string `json:"last_played"`
}

// ClientsResponse 是今日客户端列表接口载荷。
type ClientsResponse struct {
	Date    string       `json:"date"`
	Clients []ClientInfo `json:"clients"`
	Total   int          `json:"total"`
}

// todayBytesFromBuffer 快照内存缓冲中指定日期的流量。
// 缓冲中尚未落盘的字节与数据库已落盘字节互斥存在，二者相加即为全量。
func (s *Store) todayBytesFromBuffer(date string) (bytes int64) {
	s.trafficMu.Lock()
	defer s.trafficMu.Unlock()
	if entry, ok := s.traffic[date]; ok {
		bytes = entry.Bytes
	}
	return bytes
}

// Stats 汇总今日与历史播放、流量、设备与节点数。
// 全程持有数据库锁后再快照缓冲：刷盘事务同样需要该锁，
// 因此同一批字节不可能同时出现在内存与数据库统计中。
func (s *Store) Stats(today string) (StatsResponse, error) {
	var resp StatsResponse
	err := s.withDB(func(db *sql.DB) error {
		buffered := s.todayBytesFromBuffer(today)

		queries := []struct {
			name  string
			query string
			args  []any
			value *int64
		}{
			{"today plays", "SELECT COUNT(*) FROM play_events WHERE play_date = ?", []any{today}, &resp.TodayPlays},
			{"total plays", "SELECT COUNT(*) FROM play_events", nil, &resp.TotalPlays},
			{"today clients", "SELECT COUNT(DISTINCT client_ip) FROM play_events WHERE play_date = ?", []any{today}, &resp.TodayClients},
			{"total clients", "SELECT COUNT(DISTINCT client_ip) FROM play_events", nil, &resp.TotalClients},
			{"today hosts", "SELECT COUNT(DISTINCT target_host) FROM play_events WHERE play_date = ?", []any{today}, &resp.TodayHosts},
			{"total hosts", "SELECT COUNT(DISTINCT target_host) FROM play_events", nil, &resp.TotalHosts},
		}
		for _, item := range queries {
			if err := db.QueryRow(item.query, item.args...).Scan(item.value); err != nil {
				return fmt.Errorf("read %s: %w", item.name, err)
			}
		}

		var dbTodayBytes sql.NullInt64
		if err := db.QueryRow("SELECT bytes FROM daily_traffic WHERE date = ?", today).Scan(&dbTodayBytes); err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("read today traffic: %w", err)
		}
		var dbTotalBytes sql.NullInt64
		if err := db.QueryRow("SELECT SUM(bytes) FROM daily_traffic").Scan(&dbTotalBytes); err != nil {
			return fmt.Errorf("read total traffic: %w", err)
		}

		resp.TodayBytes = buffered + dbTodayBytes.Int64
		resp.TotalBytes = buffered + dbTotalBytes.Int64
		resp.TodayTrafficFmt = FormatBytes(resp.TodayBytes)
		resp.TotalTrafficFmt = FormatBytes(resp.TotalBytes)
		resp.Date = today
		resp.Status = "online"
		return nil
	})
	return resp, err
}

// TodayClients 返回今日按 设备×IP×节点 聚合的播放明细。
func (s *Store) TodayClients(today string) (ClientsResponse, error) {
	var resp ClientsResponse
	err := s.withDB(func(db *sql.DB) error {
		rows, err := db.Query(`
			SELECT
				client_ip,
				COALESCE(NULLIF(device_name, ''), '未知设备') AS device_name,
				target_host,
				COUNT(*) AS play_count,
				MAX(played_at) AS last_played
			FROM play_events
			WHERE play_date = ?
			GROUP BY client_ip, target_host, COALESCE(NULLIF(device_name, ''), '未知设备')
			ORDER BY play_count DESC, last_played DESC
		`, today)
		if err != nil {
			return fmt.Errorf("query today clients: %w", err)
		}
		defer rows.Close()

		clients := make([]ClientInfo, 0)
		for rows.Next() {
			var c ClientInfo
			if err := rows.Scan(&c.ClientIP, &c.DeviceName, &c.TargetHost, &c.PlayCount, &c.LastPlayed); err != nil {
				return fmt.Errorf("scan client row: %w", err)
			}
			clients = append(clients, c)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate clients: %w", err)
		}
		resp = ClientsResponse{Date: today, Clients: clients, Total: len(clients)}
		return nil
	})
	return resp, err
}

// TrafficSlice 是流量分布中的一行（按客户端或按节点聚合）。
type TrafficSlice struct {
	Key      string `json:"key"`
	Bytes    int64  `json:"bytes"`
	BytesFmt string `json:"bytes_fmt"`
	Requests int64  `json:"requests"`
}

// TrafficBreakdown 是今日流量分布接口载荷。
// 明细表自上线起开始累积，历史日期无数据。
type TrafficBreakdown struct {
	Date     string         `json:"date"`
	ByClient []TrafficSlice `json:"by_client"`
	ByHost   []TrafficSlice `json:"by_host"`
}

// Breakdown 汇总今日按客户端与按节点的流量分布。
// 与 Stats 相同的锁序：持库锁期间快照明细缓冲，避免双算。
func (s *Store) Breakdown(today string) (TrafficBreakdown, error) {
	var resp TrafficBreakdown
	err := s.withDB(func(db *sql.DB) error {
		resp.Date = today

		s.trafficMu.Lock()
		buffered := make(map[trafficDetailKey]TrafficEntry, len(s.trafficDetail))
		for key, entry := range s.trafficDetail {
			if key.date == today {
				buffered[key] = *entry
			}
		}
		s.trafficMu.Unlock()

		byClient := make(map[string]*TrafficEntry)
		byHost := make(map[string]*TrafficEntry)
		accumulate := func(key trafficDetailKey, entry TrafficEntry) {
			if c, ok := byClient[key.client]; ok {
				c.Bytes += entry.Bytes
				c.Requests += entry.Requests
			} else {
				byClient[key.client] = &TrafficEntry{Bytes: entry.Bytes, Requests: entry.Requests}
			}
			if h, ok := byHost[key.target]; ok {
				h.Bytes += entry.Bytes
				h.Requests += entry.Requests
			} else {
				byHost[key.target] = &TrafficEntry{Bytes: entry.Bytes, Requests: entry.Requests}
			}
		}
		for key, entry := range buffered {
			accumulate(key, entry)
		}

		rows, err := db.Query(`
			SELECT client_ip, target_host, bytes, requests
			FROM traffic_by_client WHERE date = ?
		`, today)
		if err != nil {
			return fmt.Errorf("query traffic breakdown: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var key trafficDetailKey
			var entry TrafficEntry
			key.date = today
			if err := rows.Scan(&key.client, &key.target, &entry.Bytes, &entry.Requests); err != nil {
				return fmt.Errorf("scan traffic breakdown row: %w", err)
			}
			accumulate(key, entry)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate traffic breakdown: %w", err)
		}

		resp.ByClient = slicesFromAgg(byClient)
		resp.ByHost = slicesFromAgg(byHost)
		return nil
	})
	return resp, err
}

func slicesFromAgg(agg map[string]*TrafficEntry) []TrafficSlice {
	out := make([]TrafficSlice, 0, len(agg))
	for key, entry := range agg {
		out = append(out, TrafficSlice{
			Key:      key,
			Bytes:    entry.Bytes,
			BytesFmt: FormatBytes(entry.Bytes),
			Requests: entry.Requests,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out
}

// TrendPoint 是趋势图中的一天。
type TrendPoint struct {
	Date     string `json:"date"`
	Plays    int64  `json:"plays"`
	Bytes    int64  `json:"bytes"`
	BytesFmt string `json:"bytes_fmt"`
}

// Trend 返回最近 days 天（含今日）的播放与流量趋势，按日期升序。
func (s *Store) Trend(days int) ([]TrendPoint, error) {
	if days < 1 {
		days = 1
	}
	if days > 365 {
		days = 365
	}
	today := clock.Today()
	start := clock.Now().AddDate(0, 0, -(days - 1)).Format("2006-01-02")

	byDate := make(map[string]*TrendPoint, days)
	for i := 0; i < days; i++ {
		date := clock.Now().AddDate(0, 0, -i).Format("2006-01-02")
		byDate[date] = &TrendPoint{Date: date}
	}

	err := s.withDB(func(db *sql.DB) error {
		rows, err := db.Query(`
			SELECT date, bytes FROM daily_traffic WHERE date >= ? AND date <= ?
		`, start, today)
		if err != nil {
			return fmt.Errorf("query traffic trend: %w", err)
		}
		for rows.Next() {
			var date string
			var bytes int64
			if err := rows.Scan(&date, &bytes); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan traffic trend row: %w", err)
			}
			if p, ok := byDate[date]; ok {
				p.Bytes = bytes
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate traffic trend: %w", err)
		}
		_ = rows.Close()

		playRows, err := db.Query(`
			SELECT play_date, COUNT(*) FROM play_events
			WHERE play_date >= ? AND play_date <= ? GROUP BY play_date
		`, start, today)
		if err != nil {
			return fmt.Errorf("query play trend: %w", err)
		}
		defer playRows.Close()
		for playRows.Next() {
			var date string
			var plays int64
			if err := playRows.Scan(&date, &plays); err != nil {
				return fmt.Errorf("scan play trend row: %w", err)
			}
			if p, ok := byDate[date]; ok {
				p.Plays = plays
			}
		}
		return playRows.Err()
	})
	if err != nil {
		return nil, err
	}

	out := make([]TrendPoint, 0, days)
	for i := days - 1; i >= 0; i-- {
		date := clock.Now().AddDate(0, 0, -i).Format("2006-01-02")
		p := byDate[date]
		p.BytesFmt = FormatBytes(p.Bytes)
		out = append(out, *p)
	}
	return out, nil
}
