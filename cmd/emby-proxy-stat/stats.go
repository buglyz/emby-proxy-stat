package main

import (
	"database/sql"
	"fmt"
)

func getStats() (StatsResponse, error) {
	todayStr := businessNow().Format("2006-01-02")
	var todayPlays, totalPlays, todayBytes, totalBytes int64
	var todayClients, totalClients, todayHosts, totalHosts int64

	trafficMu.Lock()
	if entry, ok := trafficBuffer[todayStr]; ok {
		todayBytes += entry.Bytes
		totalBytes += entry.Bytes
	}
	trafficMu.Unlock()

	dbMu.Lock()
	defer dbMu.Unlock()
	queries := []struct {
		name  string
		query string
		args  []any
		value *int64
	}{
		{"today plays", "SELECT COUNT(*) FROM play_events WHERE play_date = ?", []any{todayStr}, &todayPlays},
		{"total plays", "SELECT COUNT(*) FROM play_events", nil, &totalPlays},
		{"today clients", "SELECT COUNT(DISTINCT client_ip) FROM play_events WHERE play_date = ?", []any{todayStr}, &todayClients},
		{"total clients", "SELECT COUNT(DISTINCT client_ip) FROM play_events", nil, &totalClients},
		{"today hosts", "SELECT COUNT(DISTINCT target_host) FROM play_events WHERE play_date = ?", []any{todayStr}, &todayHosts},
		{"total hosts", "SELECT COUNT(DISTINCT target_host) FROM play_events", nil, &totalHosts},
	}
	for _, item := range queries {
		if err := db.QueryRow(item.query, item.args...).Scan(item.value); err != nil {
			return StatsResponse{}, fmt.Errorf("read %s: %w", item.name, err)
		}
	}

	var dbTodayBytes sql.NullInt64
	if err := db.QueryRow("SELECT bytes FROM daily_traffic WHERE date = ?", todayStr).Scan(&dbTodayBytes); err != nil && err != sql.ErrNoRows {
		return StatsResponse{}, fmt.Errorf("read today traffic: %w", err)
	}
	if dbTodayBytes.Valid {
		todayBytes += dbTodayBytes.Int64
	}

	var dbTotalBytes sql.NullInt64
	if err := db.QueryRow("SELECT SUM(bytes) FROM daily_traffic").Scan(&dbTotalBytes); err != nil {
		return StatsResponse{}, fmt.Errorf("read total traffic: %w", err)
	}
	if dbTotalBytes.Valid {
		totalBytes += dbTotalBytes.Int64
	}

	return StatsResponse{
		TodayPlays:      todayPlays,
		TotalPlays:      totalPlays,
		TodayBytes:      todayBytes,
		TotalBytes:      totalBytes,
		TodayTrafficFmt: formatBytes(todayBytes),
		TotalTrafficFmt: formatBytes(totalBytes),
		TodayClients:    todayClients,
		TotalClients:    totalClients,
		TodayHosts:      todayHosts,
		TotalHosts:      totalHosts,
		Date:            todayStr,
		Status:          "online",
	}, nil
}
