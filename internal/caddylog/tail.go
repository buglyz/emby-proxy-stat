package caddylog

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"emby-proxy-stat/internal/clock"
	"emby-proxy-stat/internal/store"
)

// Recorder 是 tail 工作流依赖的存储接口（*store.Store 天然满足）。
type Recorder interface {
	AddTraffic(date, clientIP, targetHost string, sizeBytes int64)
	RecordPlay(ev store.PlayEvent) error
}

// SessionInfo 是一条实时观看会话的快照。
type SessionInfo struct {
	TargetHost     string `json:"target_host"`
	DeviceName     string `json:"device_name"`
	ItemID         string `json:"item_id"`
	LastSeenSecAgo int    `json:"last_seen_seconds_ago"`
	Active         bool   `json:"active"` // 最后一次媒体流请求是否仍在心跳窗口内
}

const (
	streamBindingWindow = 120 * time.Second // 心跳与媒体流的最大关联间隔
	streamStaleAfter    = 10 * time.Minute  // 无心跳媒体流的回收时间
	pruneInterval       = 2 * time.Minute
	followPollInterval  = 200 * time.Millisecond
)

// activeStream 记录某 设备×上游 最近一次媒体流请求，用于心跳关联。
// 移动运营商 CGNAT 下客户端 IP 会在会话内漂移，若按 IP 绑定，
// 心跳会频繁找不到媒体流而退化为 session 级记录；设备名来自
// Emby 鉴权头，不随 IP 变化，故作主键，IP 键仅作无设备名时的兜底。
type activeStream struct {
	itemID     string
	uri        string
	deviceName string
	seenAt     time.Time
}

const unknownDevice = "未知设备"

func deviceKey(device, host string) string { return device + "|" + host }

func ipKey(ip, host string) string { return ip + "|" + host }

// Tailer 持续跟踪 Caddy 访问日志文件，处理轮转与截断。
type Tailer struct {
	rec  Recorder
	path string

	streamMu      sync.Mutex
	activeStreams map[string]activeStream // clientIP:targetHost -> stream

	lastPrune time.Time
	started   bool // 是否已完成首次定位（启动时跳过历史，轮转重开需从头读）
}

// NewTailer 创建日志跟踪器。
func NewTailer(rec Recorder, path string) *Tailer {
	return &Tailer{
		rec:           rec,
		path:          path,
		activeStreams: make(map[string]activeStream),
	}
}

// Run 阻塞运行直到 ctx 取消；内部错误自动退避重试。
func (t *Tailer) Run(ctx context.Context) {
	for {
		if err := t.followOnce(ctx); err != nil && ctx.Err() == nil {
			log.Printf("[LogTail] %v, retry in 1s", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// followOnce 打开日志文件并跟踪到文件末尾，处理轮转/截断后返回。
func (t *Tailer) followOnce(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic recovered: %v", r)
		}
	}()

	file, err := os.Open(t.path)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	defer file.Close()

	fi, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat log: %w", err)
	}

	if !t.started {
		// 进程启动：跳过既有历史，只统计启动后的新流量
		_, _ = file.Seek(0, io.SeekEnd)
		t.started = true
	}

	var offset int64
	reader := bufio.NewReader(file)

	for {
		line, readErr := reader.ReadBytes('\n')
		if readErr == io.EOF {
			if ctxErr := sleepContext(ctx, followPollInterval); ctxErr != nil {
				return nil
			}
			newFi, statErr := os.Stat(t.path)
			if statErr != nil || !os.SameFile(fi, newFi) || offset > newFi.Size() {
				return nil // 轮转或截断，外层重开
			}
			continue
		}
		if readErr != nil {
			return fmt.Errorf("read log: %w", readErr)
		}
		offset += int64(len(line))

		t.processLine(line)
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (t *Tailer) processLine(line []byte) {
	var entry Entry
	if err := json.Unmarshal(line, &entry); err != nil {
		return // 非 JSON 行（如 Caddy 启动日志）直接跳过
	}

	uri := entry.Request.URI
	if uri == "/" || uri == "/index.html" || uri == "/favicon.ico" ||
		len(uri) >= 5 && uri[:5] == "/api/" {
		return
	}
	// 只统计成功响应（2xx/3xx），错误响应不产生有效播放与流量
	if entry.Status < 200 || entry.Status >= 400 {
		return
	}

	var logTime time.Time
	if entry.TS > 0 {
		sec := int64(entry.TS)
		nsec := int64((entry.TS - float64(sec)) * 1e9)
		logTime = time.Unix(sec, nsec)
	} else {
		logTime = clock.Now()
	}

	ip := clientIP(entry)
	host := ExtractTargetHost(uri)
	device := ExtractDeviceInfo(entry)

	if entry.Size > 0 {
		t.rec.AddTraffic(logTime.Format("2006-01-02"), ip, host, entry.Size)
	}

	if itemID, ok := matchStream(uri); ok {
		t.rememberStream(ip, host, device, itemID, uri)
	}
	if progressPattern.MatchString(uri) {
		t.recordHeartbeat(ip, host, uri, device, logTime)
	}

	t.pruneStreamsMaybe()
}

// matchStream 判断 URI 是否为媒体流请求并返回其 ItemID。
func matchStream(uri string) (string, bool) {
	if m := videoStreamPattern.FindStringSubmatch(uri); len(m) > 1 {
		return m[1], true
	}
	if m := audioStreamPattern.FindStringSubmatch(uri); len(m) > 1 {
		return m[1], true
	}
	return "", false
}

// rememberStream 把媒体流请求同时登记在设备键与 IP 键下：
// 带设备名的心跳走设备键，无设备名的心跳走 IP 键兜底。
func (t *Tailer) rememberStream(ip, host, device, itemID, uri string) {
	stream := activeStream{itemID: itemID, uri: uri, deviceName: device, seenAt: time.Now()}
	t.streamMu.Lock()
	t.activeStreams[deviceKey(device, host)] = stream
	if device != unknownDevice {
		t.activeStreams[ipKey(ip, host)] = stream
	}
	t.streamMu.Unlock()
}

// recordHeartbeat 处理播放心跳：优先关联最近媒体流的 ItemID 与设备名。
func (t *Tailer) recordHeartbeat(ip, host, uri, device string, logTime time.Time) {
	t.streamMu.Lock()
	stream, hasStream := t.activeStreams[deviceKey(device, host)]
	if !hasStream {
		stream, hasStream = t.activeStreams[ipKey(ip, host)]
	}
	t.streamMu.Unlock()

	finalDevice := device
	if (finalDevice == "" || finalDevice == "未知设备") && hasStream &&
		stream.deviceName != "" && stream.deviceName != "未知设备" {
		finalDevice = stream.deviceName
	}

	itemID, itemURI := "session", uri
	if hasStream && time.Since(stream.seenAt) <= streamBindingWindow {
		itemID, itemURI = stream.itemID, stream.uri
	}

	if err := t.rec.RecordPlay(store.PlayEvent{
		ClientIP:   ip,
		TargetHost: host,
		ItemID:     itemID,
		URI:        itemURI,
		DeviceName: finalDevice,
		LogTime:    logTime,
	}); err != nil {
		log.Printf("[LogTail] record play: %v", err)
	}
}

// ActiveSessions 返回当前已登记的观看会话快照（含仍在心跳窗口内的活跃会话）。
// 同一媒体流会同时登记在设备键与 IP 键下，这里按流身份去重。
func (t *Tailer) ActiveSessions() []SessionInfo {
	now := time.Now()
	t.streamMu.Lock()
	out := make([]SessionInfo, 0, len(t.activeStreams))
	seen := make(map[string]struct{}, len(t.activeStreams))
	for _, stream := range t.activeStreams {
		identity := fmt.Sprintf("%s|%s|%s|%d", stream.deviceName, stream.itemID, stream.uri, stream.seenAt.UnixNano())
		if _, dup := seen[identity]; dup {
			continue
		}
		seen[identity] = struct{}{}
		secondsAgo := int(now.Sub(stream.seenAt).Seconds())
		out = append(out, SessionInfo{
			TargetHost:     hostOnly(stream.uri),
			DeviceName:     stream.deviceName,
			ItemID:         stream.itemID,
			LastSeenSecAgo: secondsAgo,
			Active:         secondsAgo <= int(streamBindingWindow.Seconds()),
		})
	}
	t.streamMu.Unlock()
	return out
}

// hostOnly 从网关路径里提取上游地址用于展示。
func hostOnly(uri string) string {
	return ExtractTargetHost(uri)
}

// pruneStreamsMaybe 周期回收超时未被心跳关联的媒体流记录。
func (t *Tailer) pruneStreamsMaybe() {
	now := time.Now()
	if now.Sub(t.lastPrune) < pruneInterval {
		return
	}
	t.lastPrune = now
	t.streamMu.Lock()
	for key, stream := range t.activeStreams {
		if now.Sub(stream.seenAt) > streamStaleAfter {
			delete(t.activeStreams, key)
		}
	}
	t.streamMu.Unlock()
}
