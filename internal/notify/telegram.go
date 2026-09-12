// Package notify 提供 Telegram 运营播报：消息发送、报告模板与每日调度。
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// Sender 是 Telegram Bot API 客户端；未启用时 Send 恒返回 false。
type Sender struct {
	enabled bool
	apiURL  string
	chatID  string
	client  *http.Client
}

// NewSender 创建发送器；enabled 为 false 时构建空对象即可安全调用。
func NewSender(enabled bool, botToken, chatID string) *Sender {
	s := &Sender{
		enabled: enabled,
		chatID:  chatID,
		client:  &http.Client{Timeout: 15 * time.Second},
	}
	if enabled && botToken != "" && chatID != "" {
		s.apiURL = fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)
	} else {
		s.enabled = false
	}
	return s
}

// Enabled 报告 Telegram 播报是否可用。
func (s *Sender) Enabled() bool { return s.enabled }

// Send 发送 HTML 消息并返回是否成功。
func (s *Sender) Send(text string) bool {
	if !s.enabled {
		return false
	}
	payload := map[string]any{
		"chat_id":                  s.chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[TG Error Encode] %v", err)
		return false
	}
	resp, err := s.client.Post(s.apiURL, "application/json", bytes.NewReader(body))
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
