package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const proxyGuardAddr = "127.0.0.1:8998"

var (
	errBlockedUpstream = errors.New("upstream address is not allowed")
	carrierGradeNAT    = mustCIDR("100.64.0.0/10")
	proxyTransport     = &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		DialContext:           dialPinnedProxyAddress,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
	}
)

type proxyDialKey struct{}

type proxyTarget struct {
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

func startProxyGuard() {
	listener, err := net.Listen("tcp", proxyGuardAddr)
	if err != nil {
		log.Fatalf("[Fatal] proxy guard listen failed: %v", err)
	}
	server := &http.Server{
		Addr:              proxyGuardAddr,
		Handler:           http.HandlerFunc(handleProxyGuard),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	go func() {
		log.Printf("[Proxy Guard] Running on %s", proxyGuardAddr)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[Fatal] proxy guard stopped: %v", err)
		}
	}()
}

func handleProxyGuard(w http.ResponseWriter, r *http.Request) {
	target, err := parseProxyTarget(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid upstream target"})
		return
	}

	lookupContext, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	resolvedIP, err := resolvePublicUpstream(lookupContext, target.hostname)
	cancel()
	if err != nil {
		if errors.Is(err, errBlockedUpstream) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "internal upstream addresses are blocked"})
			return
		}
		log.Printf("[Proxy DNS Error] host=%s err=%v", target.hostname, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream resolution failed"})
		return
	}

	dialAddress := net.JoinHostPort(resolvedIP.String(), target.port)
	proxy := &httputil.ReverseProxy{
		Transport:     proxyTransport,
		FlushInterval: -1,
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(&url.URL{Scheme: target.scheme, Host: target.host})
			request.Out.URL.Path = target.path
			request.Out.URL.RawPath = target.rawPath
			request.Out.URL.RawQuery = target.rawQuery
			request.Out.Host = target.host
			request.Out = request.Out.WithContext(context.WithValue(request.Out.Context(), proxyDialKey{}, dialAddress))
			if clientIP := loginClientKey(r); clientIP != "unknown" {
				request.Out.Header.Set("X-Real-IP", clientIP)
			}
		},
		ModifyResponse: func(response *http.Response) error {
			return rewriteProxyLocation(response, target)
		},
		ErrorHandler: func(writer http.ResponseWriter, _ *http.Request, proxyErr error) {
			log.Printf("[Proxy Error] host=%s err=%v", target.hostname, proxyErr)
			writeJSON(writer, http.StatusBadGateway, map[string]string{"error": "upstream request failed"})
		},
	}
	proxy.ServeHTTP(w, r)
}

func parseProxyTarget(r *http.Request) (proxyTarget, error) {
	raw := strings.TrimPrefix(r.URL.EscapedPath(), "/")
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return proxyTarget{}, fmt.Errorf("unsupported proxy scheme")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Fragment != "" {
		return proxyTarget{}, fmt.Errorf("invalid proxy URL")
	}
	hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if hostname == "" {
		return proxyTarget{}, fmt.Errorf("missing upstream hostname")
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
			return proxyTarget{}, fmt.Errorf("invalid upstream port")
		}
	}
	path := parsed.Path
	if path == "" {
		path = "/"
	}
	return proxyTarget{
		scheme:   parsed.Scheme,
		host:     parsed.Host,
		hostname: hostname,
		port:     port,
		path:     path,
		rawPath:  parsed.RawPath,
		rawQuery: r.URL.RawQuery,
	}, nil
}

func resolvePublicUpstream(ctx context.Context, hostname string) (net.IP, error) {
	addresses, err := net.DefaultResolver.LookupIP(ctx, "ip", hostname)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("no address for %s", hostname)
	}
	for _, address := range addresses {
		if isBlockedUpstreamIP(address) {
			return nil, fmt.Errorf("%w: %s resolves to %s", errBlockedUpstream, hostname, address)
		}
	}
	return addresses[0], nil
}

func isBlockedUpstreamIP(address net.IP) bool {
	if address == nil || address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() || address.IsUnspecified() || address.IsMulticast() {
		return true
	}
	if ipv4 := address.To4(); ipv4 != nil && carrierGradeNAT.Contains(ipv4) {
		return true
	}
	return false
}

func dialPinnedProxyAddress(ctx context.Context, network, _ string) (net.Conn, error) {
	dialAddress, ok := ctx.Value(proxyDialKey{}).(string)
	if !ok || dialAddress == "" {
		return nil, fmt.Errorf("missing validated upstream address")
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return dialer.DialContext(ctx, network, dialAddress)
}

func rewriteProxyLocation(response *http.Response, target proxyTarget) error {
	location := response.Header.Get("Location")
	if location == "" {
		return nil
	}
	if strings.HasPrefix(location, "//") {
		if parsed, err := url.Parse(target.scheme + ":" + location); err == nil {
			location = parsed.String()
		}
	}
	if parsed, err := url.Parse(location); err == nil && parsed.IsAbs() && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		if strings.EqualFold(parsed.Hostname(), "auto.fleey.de") {
			return nil
		}
		response.Header.Set("Location", gatewayLocation(parsed.Scheme, parsed.Host, parsed.RequestURI()))
		return nil
	}
	if strings.HasPrefix(location, "/") {
		response.Header.Set("Location", gatewayLocation(target.scheme, target.host, location))
	} else {
		response.Header.Set("Location", gatewayLocation(target.scheme, target.host, "/"+location))
	}
	return nil
}

func gatewayLocation(scheme, host, suffix string) string {
	return "https://auto.fleey.de/" + scheme + "://" + host + suffix
}
