package main

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"os"
	"strings"
	"time"
)

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
			if vMatches := videoStreamPattern.FindStringSubmatch(uri); len(vMatches) > 1 {
				streamMu.Lock()
				activeStreams[pairKey] = ActiveStream{ItemID: vMatches[1], URI: uri, SeenAt: time.Now()}
				streamMu.Unlock()
			} else if aMatches := audioStreamPattern.FindStringSubmatch(uri); len(aMatches) > 1 {
				streamMu.Lock()
				activeStreams[pairKey] = ActiveStream{ItemID: aMatches[1], URI: uri, SeenAt: time.Now()}
				streamMu.Unlock()
			}

			if progressPattern.MatchString(uri) {
				streamMu.Lock()
				as, hasStream := activeStreams[pairKey]
				streamMu.Unlock()
				if hasStream && time.Since(as.SeenAt) <= 120*time.Second {
					if err := recordPlay(clientIP, targetHost, as.ItemID, as.URI, logTime); err != nil {
						log.Printf("[DB Error recordPlay] %v", err)
					}
				} else if err := recordPlay(clientIP, targetHost, "session", uri, logTime); err != nil {
					log.Printf("[DB Error recordPlay] %v", err)
				}
			}
		}
	}
}
