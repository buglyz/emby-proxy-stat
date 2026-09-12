// Package netutil 提供客户端 IP 归一化与可信链尾提取。
//
// Caddy 是唯一可信反向前置，会把真实客户端 IP 追加在 X-Forwarded-For
// 链尾；链首内容可被客户端伪造，因此任何取信场景都必须取末位 IP，
// 否则登录限流与统计均可被伪造头绕过。
package netutil

import (
	"net"
	"net/http"
	"strings"
)

// NormalizeIP 清洗原始 IP 字符串：去空白、去端口、校验合法后返回规范形式。
func NormalizeIP(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	if ip := net.ParseIP(value); ip != nil {
		return ip.String()
	}
	return ""
}

// LastForwardedIP 从 X-Forwarded-For 值列表中取最后一个可解析 IP（链尾）。
func LastForwardedIP(values []string) string {
	for i := len(values) - 1; i >= 0; i-- {
		parts := strings.Split(values[i], ",")
		for j := len(parts) - 1; j >= 0; j-- {
			if normalized := NormalizeIP(parts[j]); normalized != "" {
				return normalized
			}
		}
	}
	return ""
}

// HeaderValues 大小写无关地读取 headers 中的指定头。
func HeaderValues(headers map[string][]string, name string) []string {
	for key, values := range headers {
		if strings.EqualFold(key, name) {
			return values
		}
	}
	return nil
}

// ForwardedIPFromHeaders 从日志/请求头 map 中提取可信链尾客户端 IP。
func ForwardedIPFromHeaders(headers map[string][]string) string {
	return LastForwardedIP(HeaderValues(headers, "X-Forwarded-For"))
}

// ClientKeyFromRequest 提取请求的真实客户端 IP：优先 XFF 链尾，回退 RemoteAddr。
func ClientKeyFromRequest(r *http.Request) string {
	if ip := LastForwardedIP(r.Header.Values("X-Forwarded-For")); ip != "" {
		return ip
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if ip := NormalizeIP(host); ip != "" {
			return ip
		}
	}
	if ip := NormalizeIP(r.RemoteAddr); ip != "" {
		return ip
	}
	return "unknown"
}
