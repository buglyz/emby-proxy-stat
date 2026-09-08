package main

import (
	"fmt"
)

type ClientInfo struct {
	ClientIP   string `json:"client_ip"`
	DeviceName string `json:"device_name"`
	TargetHost string `json:"target_host"`
	PlayCount  int64  `json:"play_count"`
	LastPlayed string `json:"last_played"`
}

type ClientsResponse struct {
	Date    string       `json:"date"`
	Clients []ClientInfo `json:"clients"`
	Total   int          `json:"total"`
}

func getTodayClients() (ClientsResponse, error) {
	todayStr := businessNow().Format("2006-01-02")

	dbMu.Lock()
	defer dbMu.Unlock()

	rows, err := db.Query(`
		SELECT 
			client_ip,
			COALESCE(NULLIF(device_name, ''), '未知设备') as device_name,
			target_host,
			COUNT(*) as play_count,
			MAX(played_at) as last_played
		FROM play_events 
		WHERE play_date = ?
		GROUP BY client_ip, target_host, COALESCE(NULLIF(device_name, ''), '未知设备')
		ORDER BY play_count DESC, last_played DESC
	`, todayStr)
	if err != nil {
		return ClientsResponse{}, fmt.Errorf("query today clients: %w", err)
	}
	defer rows.Close()

	clients := make([]ClientInfo, 0)
	for rows.Next() {
		var c ClientInfo
		if err := rows.Scan(&c.ClientIP, &c.DeviceName, &c.TargetHost, &c.PlayCount, &c.LastPlayed); err != nil {
			return ClientsResponse{}, fmt.Errorf("scan client row: %w", err)
		}
		clients = append(clients, c)
	}

	if err := rows.Err(); err != nil {
		return ClientsResponse{}, fmt.Errorf("iterate clients: %w", err)
	}

	return ClientsResponse{
		Date:    todayStr,
		Clients: clients,
		Total:   len(clients),
	}, nil
}
