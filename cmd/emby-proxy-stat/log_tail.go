package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

var (
	embyAuthDevicePattern = regexp.MustCompile(`(?i)\bDevice="?([^",]+)"?`)
	embyAuthClientPattern = regexp.MustCompile(`(?i)\bClient="?([^",]+)"?`)
)

func cleanDeviceString(s string) string {
	s = strings.Trim(s, `"' `)
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

func extractDeviceInfo(entry CaddyLogEntry) string {
	var device, client string

	// 1. 从 Headers 中的 Authorization 头提取
	for k, vals := range entry.Request.Headers {
		if strings.EqualFold(k, "X-Emby-Authorization") ||
			strings.EqualFold(k, "Authorization") ||
			strings.EqualFold(k, "X-MediaBrowser-Token") ||
			strings.Contains(strings.ToLower(k), "authorization") {
			for _, val := range vals {
				if dMatches := embyAuthDevicePattern.FindStringSubmatch(val); len(dMatches) > 1 && device == "" {
					device = strings.TrimSpace(dMatches[1])
				}
				if cMatches := embyAuthClientPattern.FindStringSubmatch(val); len(cMatches) > 1 && client == "" {
					client = strings.TrimSpace(cMatches[1])
				}
			}
		}
	}

	// 2. 从 URL Query 中提取
	if (device == "" || client == "") && strings.Contains(entry.Request.URI, "?") {
		parts := strings.SplitN(entry.Request.URI, "?", 2)
		if len(parts) == 2 {
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
	}

	// 3. 从 User-Agent 提取
	if client == "" {
		for k, vals := range entry.Request.Headers {
			if strings.EqualFold(k, "User-Agent") && len(vals) > 0 {
				ua := strings.TrimSpace(vals[0])
				if ua != "" && !strings.HasPrefix(ua, "Mozilla/") {
					client = strings.SplitN(ua, "/", 2)[0]
					client = strings.TrimSpace(client)
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

type CaddyLogEntry struct {
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

func extractTargetHost(uri string) string {
	matches := targetHostPattern.FindStringSubmatch(uri)
	if len(matches) > 1 {
		return matches[1]
	}
	return "unknown"
}

func logTailWorker() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Panic Recover] logTailWorker: %v", r)
			time.Sleep(time.Second)
			go logTailWorker()
		}
	}()

	isInitialOpen := true
	for {
		activeLogPath := getEffectiveLogPath()
		file, err := os.Open(activeLogPath)
		if err != nil {
			time.Sleep(time.Second)
			continue
		}

		fi, err := file.Stat()
		if err != nil {
			_ = file.Close()
			time.Sleep(time.Second)
			continue
		}
		if isInitialOpen {
			_, _ = file.Seek(0, io.SeekEnd)
			isInitialOpen = false
		}
		reader := bufio.NewReader(file)

		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				if err == io.EOF {
					time.Sleep(200 * time.Millisecond)
					newFi, statErr := os.Stat(activeLogPath)
					if statErr != nil || !os.SameFile(fi, newFi) || newFi.Size() < fi.Size() {
						_ = file.Close()
						break
					}
					continue
				}
				_ = file.Close()
				break
			}

			if currFi, statErr := file.Stat(); statErr == nil {
				fi = currFi
			}
			var entry CaddyLogEntry
			if jsonErr := json.Unmarshal(line, &entry); jsonErr != nil {
				continue
			}
			uri := entry.Request.URI
			if strings.HasPrefix(uri, "/api/") || uri == "/" || uri == "/index.html" || uri == "/favicon.ico" {
				continue
			}

			var logTime time.Time
			if entry.TS > 0 {
				sec := int64(entry.TS)
				nsec := int64((entry.TS - float64(sec)) * 1e9)
				logTime = time.Unix(sec, nsec).In(businessLocation)
			} else {
				logTime = businessNow()
			}
			if entry.Status < 200 || entry.Status >= 400 {
				continue
			}
			todayStr := logTime.Format("2006-01-02")
			if entry.Size > 0 {
				addTraffic(todayStr, entry.Size)
			}

			clientIP := normalizeClientIP(entry)
			targetHost := extractTargetHost(uri)
			pairKey := clientIP + ":" + targetHost
			entryDev := extractDeviceInfo(entry)

			if vMatches := videoStreamPattern.FindStringSubmatch(uri); len(vMatches) > 1 {
				streamMu.Lock()
				activeStreams[pairKey] = ActiveStream{ItemID: vMatches[1], URI: uri, DeviceName: entryDev, SeenAt: time.Now()}
				streamMu.Unlock()
			} else if aMatches := audioStreamPattern.FindStringSubmatch(uri); len(aMatches) > 1 {
				streamMu.Lock()
				activeStreams[pairKey] = ActiveStream{ItemID: aMatches[1], URI: uri, DeviceName: entryDev, SeenAt: time.Now()}
				streamMu.Unlock()
			}

			if progressPattern.MatchString(uri) {
				streamMu.Lock()
				as, hasStream := activeStreams[pairKey]
				streamMu.Unlock()

				finalDev := entryDev
				if (finalDev == "" || finalDev == "未知设备") && hasStream && as.DeviceName != "" && as.DeviceName != "未知设备" {
					finalDev = as.DeviceName
				}

				if hasStream && time.Since(as.SeenAt) <= 120*time.Second {
					if err := recordPlay(clientIP, targetHost, as.ItemID, as.URI, finalDev, logTime); err != nil {
						log.Printf("[DB Error recordPlay] %v", err)
					}
				} else if err := recordPlay(clientIP, targetHost, "session", uri, finalDev, logTime); err != nil {
					log.Printf("[DB Error recordPlay] %v", err)
				}
			}
		}
	}
}
