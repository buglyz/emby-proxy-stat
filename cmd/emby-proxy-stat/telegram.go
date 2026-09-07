package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

func sendTelegramMessage(text string) bool {
	cfg := loadConfig()
	if !cfg.Telegram.Enabled || cfg.Telegram.BotToken == "" || cfg.Telegram.ChatID == "" {
		return false
	}
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", cfg.Telegram.BotToken)
	payload := map[string]interface{}{
		"chat_id":                  cfg.Telegram.ChatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[TG Error Encode] %v", err)
		return false
	}
	req, err := http.NewRequest("POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[TG Error] %v", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		log.Printf("[TG Error Status %d] %s", resp.StatusCode, string(respBody))
		return false
	}
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		log.Printf("[TG Error Decode] %v", err)
		return false
	}
	if !result.OK {
		log.Printf("[TG Error API] %s", result.Description)
		return false
	}
	return true
}

func telegramSchedulerWorker() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Panic Recover] telegramSchedulerWorker: %v", r)
			go func() {
				time.Sleep(time.Second)
				telegramSchedulerWorker()
			}()
		}
	}()

	var lastSentDate string
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := businessNow()
		todayStr := now.Format("2006-01-02")
		cfg := loadConfig()
		if cfg.Telegram.Enabled {
			targetTime := cfg.Telegram.DailyReportTime
			if targetTime == "" {
				targetTime = "23:59"
			}
			if reportDue(now, targetTime) && lastSentDate != todayStr {
				stats, err := getStats()
				if err != nil {
					log.Printf("[TG Scheduler] stats unavailable: %v", err)
					continue
				}
				nowStr := now.Format("2006-01-02 15:04:05")
				msg := fmt.Sprintf(
					"✨ <b>Emby 网关运行日报</b>\n"+
						"━━━━━━━━━━━━━━━━━━\n"+
						"📅 <b>统计日期</b>：<code>%s</code>\n"+
						"⏰ <b>播报时间</b>：<code>%s</code>\n\n"+
						"📊 <b>【今日运营数据】</b>\n"+
						"• 🎬 <b>有效播放</b>：<code>%d</code> 次\n"+
						"• 🌐 <b>流转流量</b>：<code>%s</code>\n"+
						"• 👥 <b>活跃设备</b>：<code>%d</code> 个独立客户端\n"+
						"• 🖥️ <b>上游节点</b>：<code>%d</code> 个目标服务器\n\n"+
						"📈 <b>【历史全量汇总】</b>\n"+
						"• 🎬 <b>累计播放</b>：<code>%d</code> 次\n"+
						"• 💾 <b>累计总流量</b>：<code>%s</code>\n"+
						"• 📱 <b>累计服务设备</b>：<code>%d</code> 个\n\n"+
						"<blockquote>⚡ <b>网关状态</b>：运行正常 (Go Edition)\n"+
						"🔗 <b>控制面板</b>：<a href=\"%s\">%s</a></blockquote>",
					todayStr, nowStr,
					stats.TodayPlays, stats.TodayTrafficFmt, stats.TodayClients, stats.TodayHosts,
					stats.TotalPlays, stats.TotalTrafficFmt, stats.TotalClients,
					publicURL(), publicURL(),
				)
				if sendTelegramMessage(msg) {
					lastSentDate = todayStr
					log.Printf("[TG Scheduler] Daily report sent for %s", todayStr)
				}
			}
		}
	}
}
