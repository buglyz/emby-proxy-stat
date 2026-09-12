package netutil

import (
	"net/http/httptest"
	"testing"
)

// 回归：Caddy 会把真实客户端 IP 追加在 XFF 链尾，客户端可伪造链首，
// 必须取末位，否则登录限流与统计可被任意伪造。
func TestLastForwardedIPTakesTrustedTail(t *testing.T) {
	values := []string{"1.2.3.4, 5.6.7.8"}
	if got := LastForwardedIP(values); got != "5.6.7.8" {
		t.Fatalf("expected tail IP 5.6.7.8, got %q", got)
	}
	if got := LastForwardedIP([]string{"9.9.9.9"}); got != "9.9.9.9" {
		t.Fatalf("expected single entry 9.9.9.9, got %q", got)
	}
	if got := LastForwardedIP([]string{"not-an-ip, 5.6.7.8"}); got != "5.6.7.8" {
		t.Fatalf("expected fallback to tail valid IP, got %q", got)
	}
	if got := LastForwardedIP([]string{"garbage"}); got != "" {
		t.Fatalf("expected empty for invalid entries, got %q", got)
	}
}

func TestNormalizeIP(t *testing.T) {
	cases := map[string]string{
		" 1.2.3.4 ":     "1.2.3.4",
		"1.2.3.4:56789": "1.2.3.4",
		"[::1]:443":     "::1",
		"2001:db8::1":   "2001:db8::1",
		"":              "",
		"not-an-ip":     "",
		"300.300.300.1": "",
	}
	for input, want := range cases {
		if got := NormalizeIP(input); got != want {
			t.Errorf("NormalizeIP(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestHeaderValuesCaseInsensitive(t *testing.T) {
	headers := map[string][]string{"x-forwarded-for": {"1.1.1.1"}}
	if got := HeaderValues(headers, "X-Forwarded-For"); len(got) != 1 || got[0] != "1.1.1.1" {
		t.Fatalf("case-insensitive lookup failed: %v", got)
	}
}

func TestClientKeyFromRequestPrefersXFFTail(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/login", nil)
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 7.7.7.7")
	r.RemoteAddr = "127.0.0.1:12345"
	if got := ClientKeyFromRequest(r); got != "7.7.7.7" {
		t.Fatalf("expected 7.7.7.7 from XFF tail, got %q", got)
	}

	r2 := httptest.NewRequest("POST", "/api/login", nil)
	r2.RemoteAddr = "10.0.0.9:5555"
	if got := ClientKeyFromRequest(r2); got != "10.0.0.9" {
		t.Fatalf("expected RemoteAddr fallback, got %q", got)
	}
}

func TestClientKeyFromHeaders(t *testing.T) {
	headers := map[string][]string{"X-Forwarded-For": {"fake.example, 8.8.8.8"}}
	if got := ForwardedIPFromHeaders(headers); got != "8.8.8.8" {
		t.Fatalf("expected 8.8.8.8, got %q", got)
	}
}
