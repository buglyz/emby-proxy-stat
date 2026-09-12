package notify

import (
	"context"
	"fmt"
	"log"
	"time"

	"emby-proxy-stat/internal/clock"
	"emby-proxy-stat/internal/store"
)

const schedulerInterval = 30 * time.Second

// StatsProvider 提供调度时刻的实时统计。
type StatsProvider func() (store.StatsResponse, error)

// Scheduler 按配置时间每日推送一次运营日报。
type Scheduler struct {
	sender     *Sender
	stats      StatsProvider
	reportTime func() string // 返回当前配置的播报时间（HH:MM）
	baseURL    func() string
}

// NewScheduler 创建调度器；依赖以函数注入便于跟随配置热更新。
func NewScheduler(sender *Sender, stats StatsProvider, reportTime, baseURL func() string) *Scheduler {
	return &Scheduler{sender: sender, stats: stats, reportTime: reportTime, baseURL: baseURL}
}

// Run 阻塞运行每日播报循环，直到 ctx 取消。
func (s *Scheduler) Run(ctx context.Context) {
	var lastSentDate string
	ticker := time.NewTicker(schedulerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := clock.Now()
			today := now.Format("2006-01-02")
			if !clock.ReportDue(now, s.reportTime()) || lastSentDate == today {
				continue
			}
			stats, err := s.stats()
			if err != nil {
				log.Printf("[TG Scheduler] stats unavailable: %v", err)
				continue
			}
			msg := BuildDailyReport(stats, now, s.baseURL())
			if s.sender.Send(msg) {
				lastSentDate = today
				log.Printf("[TG Scheduler] daily report sent for %s", today)
			}
		}
	}
}

// BuildDailyReport 生成每日运营日报消息体。
func BuildDailyReport(stats store.StatsResponse, now time.Time, baseURL string) string {
	return fmt.Sprintf(
		"✨ <b>Emby 网关运行日报</b>\n"+
			"━━━━━━━━━━━━━━━━━━\n"+
			"📅 <b>统计日期</b>：<code>%s</code>\n"+
			"⏰ <b>播报时间</b>：<code>%s</code>\n\n"+
			"📊 <b>【今日运营数据】</b>\n"+
			"• 🎬 <b>有效播放</b>：<code>%d</code> 次\n"+
			"• 🌐 <b>流转流量</b>：<code>%s</code>\n"+
			"• 👥 <b>活跃设备</b>：<code>%d</code> 个独立客户端\n"+
			"• 🖥️ <b>上游节点</b>：<code>%d</code> 个目标服务器\n\n"+
			"📈 <b>【历史全量汇总】</b>\n"+
			"• 🎬 <b>累计播放</b>：<code>%d</code> 次\n"+
			"• 💾 <b>累计总流量</b>：<code>%s</code>\n"+
			"• 📱 <b>累计服务设备</b>：<code>%d</code> 个\n\n"+
			"<blockquote>⚡ <b>网关状态</b>：运行正常 (Go Edition)\n"+
			"🔗 <b>控制面板</b>：<a href=\"%s\">%s</a></blockquote>",
		stats.Date, now.Format("2006-01-02 15:04:05"),
		stats.TodayPlays, stats.TodayTrafficFmt, stats.TodayClients, stats.TodayHosts,
		stats.TotalPlays, stats.TotalTrafficFmt, stats.TotalClients,
		baseURL, hostOf(baseURL),
	)
}

// BuildTestReport 生成 Web 端手动触发的连通测试消息体。
func BuildTestReport(stats store.StatsResponse, now time.Time, baseURL string) string {
	return fmt.Sprintf(
		"🔔 <b>Emby 网关 Telegram 连通测试</b>\n"+
			"━━━━━━━━━━━━━━━━━━\n"+
			"📅 <b>测试时间</b>：<code>%s</code>\n\n"+
			"📊 <b>【实时网关概览】</b>\n"+
			"• 🎬 <b>今日播放</b>：<code>%d</code> 次\n"+
			"• 🌐 <b>今日流量</b>：<code>%s</code>\n"+
			"• 👥 <b>活跃设备</b>：<code>%d</code> 个独立客户端\n"+
			"• 🖥️ <b>覆盖节点</b>：<code>%d</code> 个目标服务器\n\n"+
			"📈 <b>【历史全量汇总】</b>\n"+
			"• 🎬 <b>累计播放</b>：<code>%d</code> 次\n"+
			"• 💾 <b>累计总流量</b>：<code>%s</code>\n"+
			"• 📱 <b>累计服务设备</b>：<code>%d</code> 个\n\n"+
			"<blockquote>🟢 <b>引擎核心</b>：Go Core (Zero-CGO)\n"+
			"⏰ <b>每日播报</b>：按配置时间自动推送\n"+
			"🔗 <b>管理控制台</b>：<a href=\"%s\">%s</a></blockquote>",
		now.Format("2006-01-02 15:04:05"),
		stats.TodayPlays, stats.TodayTrafficFmt, stats.TodayClients, stats.TodayHosts,
		stats.TotalPlays, stats.TotalTrafficFmt, stats.TotalClients,
		baseURL, hostOf(baseURL),
	)
}

// hostOf 从 base URL 中提取 host 部分作为链接显示文本。
func hostOf(baseURL string) string {
	rest := baseURL
	for _, prefix := range []string{"https://", "http://"} {
		if len(rest) > len(prefix) && rest[:len(prefix)] == prefix {
			rest = rest[len(prefix):]
			break
		}
	}
	if idx := indexByte(rest, '/'); idx >= 0 {
		rest = rest[:idx]
	}
	return rest
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
