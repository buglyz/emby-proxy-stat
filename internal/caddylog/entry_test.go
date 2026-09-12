package caddylog

import (
	"testing"
)

func TestExtractTargetHost(t *testing.T) {
	cases := map[string]string{
		"/http://192.168.1.5:8096/emby/System/Info": "192.168.1.5:8096",
		"/http://emby.example.com/emby/System/Info": "emby.example.com",
		"/https://media.example.com:8920/path?q=1":  "media.example.com:8920",
		"/http:192.168.1.5:8096/emby/System/Info":   "192.168.1.5:8096",
		"/emby/System/Info":                         "unknown",
	}
	for input, want := range cases {
		if got := ExtractTargetHost(input); got != want {
			t.Errorf("ExtractTargetHost(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMatchStream(t *testing.T) {
	if id, ok := matchStream("/Videos/abc123/stream.mkv?x=1"); !ok || id != "abc123" {
		t.Fatalf("video stream: id=%q ok=%v", id, ok)
	}
	if id, ok := matchStream("/Audio/track9/stream"); !ok || id != "track9" {
		t.Fatalf("audio stream: id=%q ok=%v", id, ok)
	}
	if _, ok := matchStream("/emby/Items/abc123"); ok {
		t.Fatal("item detail page must not match as stream")
	}
}

func TestExtractDeviceInfoFromAuthHeader(t *testing.T) {
	var e Entry
	e.Request.Headers = map[string][]string{
		"X-Emby-Authorization": {`MediaBrowser Client="Infuse", Device="iPhone 15", DeviceId="abc", Version="7.7"`},
	}
	if got := ExtractDeviceInfo(e); got != "iPhone 15 (Infuse)" {
		t.Fatalf("expected combined device info, got %q", got)
	}
}

func TestExtractDeviceInfoFromQuery(t *testing.T) {
	var e Entry
	e.Request.URI = "/emby/System/Info?DeviceName=LivingTV&Client=Jellyfin%20Web"
	if got := ExtractDeviceInfo(e); got != "LivingTV (Jellyfin Web)" {
		t.Fatalf("expected query-derived info, got %q", got)
	}
}

func TestExtractDeviceInfoUnknown(t *testing.T) {
	var e Entry
	if got := ExtractDeviceInfo(e); got != "未知设备" {
		t.Fatalf("expected 未知设备, got %q", got)
	}
}

func TestCleanDeviceString(t *testing.T) {
	if got := cleanDeviceString(`"iPad Pro"`); got != "iPad Pro" {
		t.Fatalf("expected trimmed value, got %q", got)
	}
	long := make([]byte, 100)
	for i := range long {
		long[i] = 'a'
	}
	if got := cleanDeviceString(string(long)); len(got) != 40 {
		t.Fatalf("expected truncation to 40, got %d", len(got))
	}
}
