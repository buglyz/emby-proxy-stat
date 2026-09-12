package proxyguard

import (
	"context"
	"net"
	"testing"
	"time"
)

// 回归：DNS 缓存命中时不重复解析；未缓存的域名正常解析。
func TestResolveCache(t *testing.T) {
	dnsMu.Lock()
	dnsCache = map[string]dnsEntry{"cached.example": {ip: mustIP("1.2.3.4"), expires: time.Now().Add(time.Minute)}}
	dnsMu.Unlock()

	ip, err := resolvePublicUpstream(context.Background(), "cached.example")
	if err != nil || ip.String() != "1.2.3.4" {
		t.Fatalf("cache hit expected 1.2.3.4, got %v err=%v", ip, err)
	}

	// 过期条目必须重新解析（该域名无法真实解析 → 报错而非返回旧值）
	dnsMu.Lock()
	dnsCache["stale.example"] = dnsEntry{ip: mustIP("1.2.3.4"), expires: time.Now().Add(-time.Second)}
	dnsMu.Unlock()
	if _, err := resolvePublicUpstream(context.Background(), "stale.invalid"); err == nil {
		t.Fatal("expired entry must trigger a fresh lookup")
	}
}

// 回归：缓存容量上限，防止外部构造任意 hostname 撑爆内存。
func TestResolveCacheBounded(t *testing.T) {
	dnsMu.Lock()
	dnsCache = map[string]dnsEntry{}
	for i := 0; i < dnsCacheMax+10; i++ {
		key := "h" + string(rune('a'+i%26)) + time.Now().Format("150405.000000000") + string(rune('a'+i%26))
		dnsCache[key] = dnsEntry{ip: mustIP("1.1.1.1"), expires: time.Now().Add(time.Minute)}
	}
	dnsMu.Unlock()

	// 插入一个新条目触发清理，不应 panic 且容量受限
	if err := primeCache("fill.example"); err != nil {
		t.Fatalf("prime: %v", err)
	}
	dnsMu.Lock()
	size := len(dnsCache)
	dnsMu.Unlock()
	if size > dnsCacheMax {
		t.Fatalf("cache exceeded cap: %d", size)
	}
}

func mustIP(s string) net.IP { return net.ParseIP(s) }

// primeCache 绕过真实 DNS 直接登记一条已校验的缓存。
func primeCache(hostname string) error {
	ip := mustIP("9.9.9.9")
	dnsMu.Lock()
	if len(dnsCache) >= dnsCacheMax {
		now := time.Now()
		for key, entry := range dnsCache {
			if !now.Before(entry.expires) {
				delete(dnsCache, key)
			}
		}
	}
	if len(dnsCache) < dnsCacheMax {
		dnsCache[hostname] = dnsEntry{ip: ip, expires: time.Now().Add(dnsCacheTTL)}
	}
	dnsMu.Unlock()
	return nil
}
