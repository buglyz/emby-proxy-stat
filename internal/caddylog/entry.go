// Package caddylog 解析 Caddy JSON 访问日志，识别 Emby/Jellyfin 播放行为。
//
// 播放判定策略（方案 B，杜绝详情页预加载虚高）：
// 仅当客户端发出 /Sessions/Playing/Progress 心跳时才计入有效播放；
// 心跳前 120 秒内的媒体流请求（/Videos/{id}/stream 等）用于绑定真实
// ItemID、设备名与目标上游；无媒体流上下文时回退记录 session 级事件。
package caddylog

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"emby-proxy-stat/internal/netutil"
)

var (
	videoStreamPattern = regexp.MustCompile(`(?i)/Videos/([a-zA-Z0-9\-_]+)/(?:stream|original|master|main|\w+\.\w+)`)
	audioStreamPattern = regexp.MustCompile(`(?i)/Audio/([a-zA-Z0-9\-_]+)/stream`)
	progressPattern    = regexp.MustCompile(`(?i)/Sessions/Playing/Progress`)
	targetHostPattern  = regexp.MustCompile(`^/https?:/*([A-Za-z0-9.\-_:]+)`)

	embyAuthDevicePattern = regexp.MustCompile(`(?i)\bDevice="?([^",]+)"?`)
	embyAuthClientPattern = regexp.MustCompile(`(?i)\bClient="?([^",]+)"?`)
)

// Entry 是 Caddy JSON 访问日志行中与统计相关的字段子集。
type Entry struct {
	TS      float64 `json:"ts"`
	Request struct {
		ClientIP string              `json:"client_ip"`
		RemoteIP string              `json:"remote_ip"`
		URI      string              `json:"uri"`
		Headers  map[string][]string `json:"headers"`
	} `json:"request"`
	Status int   `json:"status"`
	Size   int64 `json:"size"`
}

// ExtractTargetHost 从网关路径中提取上游主机（host 或 host:port）。
func ExtractTargetHost(uri string) string {
	if matches := targetHostPattern.FindStringSubmatch(uri); len(matches) > 1 {
		return matches[1]
	}
	return "unknown"
}

func cleanDeviceString(s string) string {
	s = strings.Trim(s, `"' `)
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// ExtractDeviceInfo 三级提取设备标识：鉴权头 → URL Query → User-Agent。
func ExtractDeviceInfo(entry Entry) string {
	var device, client string

	// 1. Emby/Jellyfin 鉴权头（MediaBrowser Token 格式携带 Device/Client 字段）
	for k, vals := range entry.Request.Headers {
		if strings.EqualFold(k, "X-Emby-Authorization") ||
			strings.EqualFold(k, "Authorization") ||
			strings.EqualFold(k, "X-MediaBrowser-Token") ||
			strings.Contains(strings.ToLower(k), "authorization") {
			for _, val := range vals {
				if m := embyAuthDevicePattern.FindStringSubmatch(val); len(m) > 1 && device == "" {
					device = strings.TrimSpace(m[1])
				}
				if m := embyAuthClientPattern.FindStringSubmatch(val); len(m) > 1 && client == "" {
					client = strings.TrimSpace(m[1])
				}
			}
		}
	}

	// 2. URL Query 参数
	if (device == "" || client == "") && strings.Contains(entry.Request.URI, "?") {
		parts := strings.SplitN(entry.Request.URI, "?", 2)
		if q, err := url.ParseQuery(parts[1]); err == nil {
			if device == "" {
				for _, k := range []string{"X-Emby-Device-Name", "DeviceName", "device_name", "deviceName"} {
					if v := q.Get(k); v != "" {
						device = strings.TrimSpace(v)
						break
					}
				}
			}
			if client == "" {
				for _, k := range []string{"X-Emby-Client", "Client", "client"} {
					if v := q.Get(k); v != "" {
						client = strings.TrimSpace(v)
						break
					}
				}
			}
		}
	}

	// 3. User-Agent 兜底（排除浏览器，取应用名部分）
	if client == "" {
		for k, vals := range entry.Request.Headers {
			if strings.EqualFold(k, "User-Agent") && len(vals) > 0 {
				ua := strings.TrimSpace(vals[0])
				if ua != "" && !strings.HasPrefix(ua, "Mozilla/") {
					client = strings.TrimSpace(strings.SplitN(ua, "/", 2)[0])
				}
				break
			}
		}
	}

	device = cleanDeviceString(device)
	client = cleanDeviceString(client)

	if device != "" && client != "" {
		if strings.EqualFold(device, client) || strings.Contains(strings.ToLower(device), strings.ToLower(client)) {
			return device
		}
		return fmt.Sprintf("%s (%s)", device, client)
	}
	if device != "" {
		return device
	}
	if client != "" {
		return client
	}
	return "未知设备"
}

// clientIP 提取日志条目的可信客户端 IP（XFF 链尾 → client_ip → remote_ip）。
func clientIP(entry Entry) string {
	if forwarded := netutil.ForwardedIPFromHeaders(entry.Request.Headers); forwarded != "" {
		return forwarded
	}
	for _, candidate := range []string{entry.Request.ClientIP, entry.Request.RemoteIP} {
		if normalized := netutil.NormalizeIP(candidate); normalized != "" {
			return normalized
		}
	}
	return "unknown"
}
