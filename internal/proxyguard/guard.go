// Package proxyguard 提供带 SSRF 防护的动态反向代理：
// 解析上游域名并校验全部解析结果为公网地址（拒绝环回/私网/链路本地/CGNAT），
// 拨号时钉扎到已校验 IP，防止通过 DNS 重绑定绕过校验。
package proxyguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"emby-proxy-stat/internal/netutil"
)

var (
	errBlockedUpstream = errors.New("upstream address is not allowed")
	carrierGradeNAT    = mustCIDR("100.64.0.0/10")
	proxyTransport     = &http.Transport{
		// ForceAttemptHTTP2 必须为 false：自定义拨号 + h2 会让 TLS 上游
		// 在 ALPN 协商成 h2，而 Go 的 Transport 不支持在 h2 上做
		// WebSocket 升级，会破坏 /embywebsocket；Caddy 现状也是纯 h1。
		Proxy:                 nil,
		ForceAttemptHTTP2:     false,
		DialContext:           dialPinnedAddress,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
	}
)

type dialKey struct{}

// target 是从路径解析出的上游目标。
type target struct {
	scheme   string
	host     string
	hostname string
	port     string
	path     string
	rawPath  string
	rawQuery string
}

func mustCIDR(value string) *net.IPNet {
	_, network, err := net.ParseCIDR(value)
	if err != nil {
		panic(err)
	}
	return network
}

// New 构造 guard HTTP 处理器；baseURL 用于 Location 改写回网关格式。
func New(baseURL string) http.Handler {
	gatewayHost := hostOf(baseURL)
	gateway := strings.TrimSuffix(baseURL, "/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle(w, r, gateway, gatewayHost)
	})
}

func handle(w http.ResponseWriter, r *http.Request, gateway, gatewayHost string) {
	tgt, err := parseTarget(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid upstream target"})
		return
	}

	lookupContext, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	resolvedIP, err := resolvePublicUpstream(lookupContext, tgt.hostname)
	cancel()
	if err != nil {
		if errors.Is(err, errBlockedUpstream) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "internal upstream addresses are blocked"})
			return
		}
		log.Printf("[Proxy DNS Error] host=%s err=%v", tgt.hostname, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream resolution failed"})
		return
	}

	dialAddress := net.JoinHostPort(resolvedIP.String(), tgt.port)
	proxy := &httputil.ReverseProxy{
		Transport:     proxyTransport,
		FlushInterval: -1,
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(&url.URL{Scheme: tgt.scheme, Host: tgt.host})
			request.Out.URL.Path = tgt.path
			request.Out.URL.RawPath = tgt.rawPath
			request.Out.URL.RawQuery = tgt.rawQuery
			request.Out.Host = tgt.host
			request.Out = request.Out.WithContext(context.WithValue(request.Out.Context(), dialKey{}, dialAddress))
			if clientIP := netutil.ClientKeyFromRequest(r); clientIP != "unknown" {
				request.Out.Header.Set("X-Real-IP", clientIP)
			}
		},
		ModifyResponse: func(response *http.Response) error {
			return rewriteLocation(response, tgt, gateway, gatewayHost)
		},
		ErrorHandler: func(writer http.ResponseWriter, _ *http.Request, proxyErr error) {
			log.Printf("[Proxy Error] host=%s err=%v", tgt.hostname, proxyErr)
			writeJSON(writer, http.StatusBadGateway, map[string]string{"error": "upstream request failed"})
		},
	}
	proxy.ServeHTTP(w, r)
}

// parseTarget 从请求路径 /http(s)://host:port/path 解析上游目标。
func parseTarget(r *http.Request) (target, error) {
	raw := strings.TrimPrefix(r.URL.EscapedPath(), "/")
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return target{}, fmt.Errorf("unsupported proxy scheme")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Fragment != "" {
		return target{}, fmt.Errorf("invalid proxy URL")
	}
	hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if hostname == "" {
		return target{}, fmt.Errorf("missing upstream hostname")
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	} else {
		portNumber, portErr := strconv.Atoi(port)
		if portErr != nil || portNumber < 1 || portNumber > 65535 {
			return target{}, fmt.Errorf("invalid upstream port")
		}
	}
	path := parsed.Path
	if path == "" {
		path = "/"
	}
	return target{
		scheme:   parsed.Scheme,
		host:     parsed.Host,
		hostname: hostname,
		port:     port,
		path:     path,
		rawPath:  parsed.RawPath,
		rawQuery: r.URL.RawQuery,
	}, nil
}

// dnsCache 缓存已通过公网校验的解析结果，避免每次回源都做 DNS 查询。
// 键来自请求路径（可被外部任意构造），容量上限防止内存被撑爆。
var (
	dnsMu       sync.Mutex
	dnsCache    = map[string]dnsEntry{}
	dnsCacheTTL = 60 * time.Second
	dnsCacheMax = 4096
)

type dnsEntry struct {
	ip      net.IP
	expires time.Time
}

// resolvePublicUpstream 解析域名并要求全部地址为公网 IP，返回首选地址。
// 仅缓存成功结果；失败立即透传，不影响故障切换。
func resolvePublicUpstream(ctx context.Context, hostname string) (net.IP, error) {
	now := time.Now()
	dnsMu.Lock()
	if entry, ok := dnsCache[hostname]; ok && now.Before(entry.expires) {
		ip := entry.ip
		dnsMu.Unlock()
		return ip, nil
	}
	dnsMu.Unlock()

	addresses, err := net.DefaultResolver.LookupIP(ctx, "ip", hostname)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("no address for %s", hostname)
	}
	for _, address := range addresses {
		if isBlockedIP(address) {
			return nil, fmt.Errorf("%w: %s resolves to %s", errBlockedUpstream, hostname, address)
		}
	}

	dnsMu.Lock()
	if len(dnsCache) >= dnsCacheMax {
		for key, entry := range dnsCache {
			if !now.Before(entry.expires) {
				delete(dnsCache, key)
			}
		}
	}
	if len(dnsCache) < dnsCacheMax {
		dnsCache[hostname] = dnsEntry{ip: addresses[0], expires: now.Add(dnsCacheTTL)}
	}
	dnsMu.Unlock()
	return addresses[0], nil
}

// isBlockedIP 判断是否为禁止访问的非公网地址。
func isBlockedIP(address net.IP) bool {
	if address == nil || address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() || address.IsUnspecified() || address.IsMulticast() {
		return true
	}
	if ipv4 := address.To4(); ipv4 != nil && carrierGradeNAT.Contains(ipv4) {
		return true
	}
	return false
}

// dialPinnedAddress 使用请求上下文中钉扎的已校验地址拨号。
func dialPinnedAddress(ctx context.Context, network, _ string) (net.Conn, error) {
	dialAddress, ok := ctx.Value(dialKey{}).(string)
	if !ok || dialAddress == "" {
		return nil, fmt.Errorf("missing validated upstream address")
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return dialer.DialContext(ctx, network, dialAddress)
}

// rewriteLocation 把上游 30x Location 改写为网关路径格式。
func rewriteLocation(response *http.Response, tgt target, gateway, gatewayHost string) error {
	location := response.Header.Get("Location")
	if location == "" {
		return nil
	}
	if strings.HasPrefix(location, "//") {
		if parsed, err := url.Parse(tgt.scheme + ":" + location); err == nil {
			location = parsed.String()
		}
	}
	if parsed, err := url.Parse(location); err == nil && parsed.IsAbs() &&
		(parsed.Scheme == "http" || parsed.Scheme == "https") {
		if strings.EqualFold(parsed.Hostname(), gatewayHost) {
			return nil // 已是网关地址，保持原样
		}
		response.Header.Set("Location", gatewayLocation(gateway, parsed.Scheme, parsed.Host, parsed.RequestURI()))
		return nil
	}
	if strings.HasPrefix(location, "/") {
		response.Header.Set("Location", gatewayLocation(gateway, tgt.scheme, tgt.host, location))
	} else {
		response.Header.Set("Location", gatewayLocation(gateway, tgt.scheme, tgt.host, "/"+location))
	}
	return nil
}

func gatewayLocation(gateway, scheme, host, suffix string) string {
	return gateway + "/" + scheme + "://" + host + suffix
}

func hostOf(baseURL string) string {
	rest := baseURL
	for _, prefix := range []string{"https://", "http://"} {
		if len(rest) > len(prefix) && rest[:len(prefix)] == prefix {
			rest = rest[len(prefix):]
			break
		}
	}
	if idx := strings.IndexByte(rest, '/'); idx >= 0 {
		rest = rest[:idx]
	}
	return rest
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
