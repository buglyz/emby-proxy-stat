package main

import (
	"net"
	"strings"
)

func normalizeClientIP(entry CaddyLogEntry) string {
	if forwarded := firstForwardedIP(entry.Request.Headers); forwarded != "" {
		return forwarded
	}
	for _, candidate := range []string{entry.Request.ClientIP, entry.Request.RemoteIP} {
		if normalized := normalizeIP(candidate); normalized != "" {
			return normalized
		}
	}
	return "unknown"
}

func firstForwardedIP(headers map[string][]string) string {
	for _, key := range []string{"X-Forwarded-For", "x-forwarded-for"} {
		values := headers[key]
		if len(values) == 0 {
			continue
		}
		for _, value := range strings.Split(values[0], ",") {
			if normalized := normalizeIP(value); normalized != "" {
				return normalized
			}
		}
	}
	return ""
}

func normalizeIP(raw string) string {
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
