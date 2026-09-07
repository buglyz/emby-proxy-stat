package main

import "fmt"

func persistTrafficBatch(batch map[string]TrafficEntry) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	stmt, err := tx.Prepare(`
		INSERT INTO daily_traffic (date, bytes, requests, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(date) DO UPDATE SET
			bytes = bytes + excluded.bytes,
			requests = requests + excluded.requests,
			updated_at = CURRENT_TIMESTAMP
	`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare traffic statement: %w", err)
	}
	for date, entry := range batch {
		if _, err := stmt.Exec(date, entry.Bytes, entry.Requests); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return fmt.Errorf("write traffic for %s: %w", date, err)
		}
	}
	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("close traffic statement: %w", err)
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("commit traffic batch: %w", err)
	}
	return nil
}

func restoreTrafficBatch(batch map[string]TrafficEntry) {
	trafficMu.Lock()
	defer trafficMu.Unlock()
	for date, entry := range batch {
		current := trafficBuffer[date]
		if current == nil {
			current = &TrafficEntry{}
			trafficBuffer[date] = current
		}
		current.Bytes += entry.Bytes
		current.Requests += entry.Requests
	}
}
