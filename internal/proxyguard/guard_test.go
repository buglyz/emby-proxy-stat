package proxyguard

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsBlockedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "::1", "10.0.0.1", "172.16.0.1", "192.168.1.1",
		"169.254.169.254", "100.64.0.1", "0.0.0.0", "224.0.0.1",
	}
	for _, value := range blocked {
		if !isBlockedIP(net.ParseIP(value)) {
			t.Errorf("expected %s to be blocked", value)
		}
	}
	if isBlockedIP(net.ParseIP("1.1.1.1")) {
		t.Fatal("public address must remain allowed")
	}
	if !isBlockedIP(nil) {
		t.Fatal("nil IP must be blocked (defensive)")
	}
}

// 回归：必须拒绝非 http(s) scheme 与缺失主机名，防 SSRF 变体。
func TestParseTargetValidation(t *testing.T) {
	bad := []string{
		"/ftp://example.com/file",
		"/http://",
		"/https://example.com:99999/x",
		"/https://user:pass@example.com/x",
	}
	for _, raw := range bad {
		r := httptest.NewRequest("GET", "https://gateway"+raw, nil)
		if _, err := parseTarget(r); err == nil {
			t.Errorf("expected %q to be rejected", raw)
		}
	}

	r := httptest.NewRequest("GET", "https://gateway/https://example.com:8443/media/file?token=abc", nil)
	tgt, err := parseTarget(r)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	if tgt.scheme != "https" || tgt.hostname != "example.com" || tgt.port != "8443" || tgt.rawQuery != "token=abc" {
		t.Fatalf("unexpected target: %+v", tgt)
	}
	if tgt.host != "example.com:8443" {
		t.Fatalf("host should keep port for Host header, got %q", tgt.host)
	}
}

func TestParseTargetDefaultsPorts(t *testing.T) {
	r := httptest.NewRequest("GET", "https://gateway/http://example.com/x", nil)
	tgt, err := parseTarget(r)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	if tgt.port != "80" {
		t.Fatalf("http default port, got %q", tgt.port)
	}

	r2 := httptest.NewRequest("GET", "https://gateway/https://example.com/x", nil)
	tgt2, err := parseTarget(r2)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	if tgt2.port != "443" {
		t.Fatalf("https default port, got %q", tgt2.port)
	}
}

func TestRewriteLocation(t *testing.T) {
	gateway := "https://auto.fleey.de"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"absolute other host", "https://cdn.example.com/a/b", gateway + "/https://cdn.example.com/a/b"},
		{"relative", "/emby/redirect", gateway + "/https://up.example:8096/emby/redirect"},
		{"bare", "emby/redirect", gateway + "/https://up.example:8096/emby/redirect"},
		{"protocol relative", "//cdn.example.com/x", gateway + "/https://cdn.example.com/x"},
		{"already gateway", gateway + "/https://x.example/y", gateway + "/https://x.example/y"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{}}
			resp.Header.Set("Location", tc.in)
			tgt := target{scheme: "https", host: "up.example:8096"}
			if err := rewriteLocation(resp, tgt, gateway, "auto.fleey.de"); err != nil {
				t.Fatalf("rewrite: %v", err)
			}
			if got := resp.Header.Get("Location"); got != tc.want {
				t.Fatalf("Location = %q, want %q", got, tc.want)
			}
		})
	}
}
