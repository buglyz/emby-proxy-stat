package main

import (
	"net"
	"net/http/httptest"
	"testing"
)

func TestIsBlockedUpstreamIP(t *testing.T) {
	blocked := []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "::1"}
	for _, value := range blocked {
		if !isBlockedUpstreamIP(net.ParseIP(value)) {
			t.Errorf("expected %s to be blocked", value)
		}
	}
	if isBlockedUpstreamIP(net.ParseIP("1.1.1.1")) {
		t.Fatal("public address must remain allowed")
	}
}

func TestParseProxyTarget(t *testing.T) {
	req := httptest.NewRequest("GET", "https://gateway/https://example.com:8443/media/file?token=abc", nil)
	target, err := parseProxyTarget(req)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	if target.scheme != "https" || target.hostname != "example.com" || target.port != "8443" || target.rawQuery != "token=abc" {
		t.Fatalf("unexpected target: %+v", target)
	}
}
