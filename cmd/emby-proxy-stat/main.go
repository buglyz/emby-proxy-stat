// emby-proxy-stat 是 Emby 代理网关的统计与控制台服务：
// 解析 Caddy 访问日志产出播放/流量统计，提供仪表盘 Web API，
// 并附带一层带 SSRF 防护的动态反向代理（127.0.0.1:8998，可选接入）。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"emby-proxy-stat/internal/auth"
	"emby-proxy-stat/internal/caddylog"
	"emby-proxy-stat/internal/clock"
	"emby-proxy-stat/internal/config"
	"emby-proxy-stat/internal/notify"
	"emby-proxy-stat/internal/proxyguard"
	"emby-proxy-stat/internal/store"
	"emby-proxy-stat/internal/web"
)

const (
	defaultConfigPath = "/opt/emby-proxy-stat/config.json"
	defaultDBPath     = "/opt/emby-proxy-stat/data/stats.db"
	defaultLogPath    = "/var/log/caddy/auto.fleey.de.log"
	defaultPort       = 8999
	proxyGuardPort    = 8998

	trafficFlushInterval = 5 * time.Second
	shutdownGracePeriod  = 5 * time.Second
)

func main() {
	configPath := flag.String("config", defaultConfigPath, "Path to config file")
	dbPath := flag.String("db", defaultDBPath, "Path to sqlite database")
	logPath := flag.String("log", defaultLogPath, "Fallback path to Caddy access log")
	port := flag.Int("port", defaultPort, "HTTP listen port")
	passwordHash := flag.Bool("password-hash", false, "Generate PBKDF2 hash for password supplied on stdin, then exit")
	flag.Parse()

	if *passwordHash {
		runPasswordHash()
		return
	}
	run(context.Background(), options{
		configPath: *configPath,
		dbPath:     *dbPath,
		logPath:    *logPath,
		port:       *port,
	})
}

type options struct {
	configPath string
	dbPath     string
	logPath    string
	port       int
}

func runPasswordHash() {
	password, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		log.Fatalf("failed to read password: %v", err)
	}
	hash, err := auth.GeneratePasswordHash(strings.TrimSuffix(string(password), "\n"))
	if err != nil {
		log.Fatalf("failed to generate password hash: %v", err)
	}
	fmt.Println(hash)
}

func run(ctx context.Context, opt options) {
	cfgMgr, err := config.NewManager(opt.configPath)
	if err != nil {
		log.Fatalf("[Fatal] load config: %v", err)
	}
	db, err := store.Open(opt.dbPath)
	if err != nil {
		log.Fatalf("[Fatal] init db: %v", err)
	}

	cfg := cfgMgr.Current()
	tailer := caddylog.NewTailer(db, effectiveLogPath(cfg, opt.logPath))
	sender := notify.NewSender(cfg.Telegram.Enabled, cfg.Telegram.BotToken, cfg.Telegram.ChatID)
	scheduler := notify.NewScheduler(
		sender,
		func() (store.StatsResponse, error) { return db.Stats(clock.Today()) },
		func() string { return cfgMgr.Current().Telegram.DailyReportTime },
		func() string { return cfgMgr.Current().BaseURL },
	)
	sessions := auth.NewSessionManager(web.SessionTTL())
	limiter := auth.NewLoginLimiter()

	// 后台任务统一挂到同一 ctx：主流程退出即全部终止
	background, cancelBackground := context.WithCancel(ctx)
	defer cancelBackground()
	go tailer.Run(background)
	go flushTrafficLoop(background, db)
	go scheduler.Run(background)
	go pruneLoop(background, db, cfgMgr)

	// 可选 SSRF 防护反代层（当前 Caddy 未接入，保留备用）
	startProxyGuard(fmt.Sprintf("127.0.0.1:%d", proxyGuardPort), cfgMgr)

	apiServer := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", opt.port),
		Handler:           web.New(web.Deps{Config: cfgMgr, Store: db, Sessions: sessions, Limiter: limiter, Sender: sender, LiveSessions: tailer.ActiveSessions}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	// SIGHUP 热重载配置；SIGTERM/SIGINT 优雅退出并冲刷流量缓冲
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for sig := range signals {
			if sig == syscall.SIGHUP {
				if err := cfgMgr.Reload(); err != nil {
					log.Printf("[Config] reload failed: %v", err)
				} else {
					log.Printf("[Config] reloaded")
				}
				continue
			}
			log.Printf("[Shutdown] %s received, draining...", sig)
			_ = apiServer.Shutdown(context.Background())
			cancelBackground()
			db.FlushTrafficOnce()
			_ = db.Close()
			os.Exit(0)
		}
	}()

	log.Printf("[Go Stats Server] running on 127.0.0.1:%d", opt.port)
	if err := apiServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("[Fatal] server error: %v", err)
	}
	// ListenAndServe 正常返回（仅测试场景直接退出）也兜底冲刷
	cancelBackground()
	db.FlushTrafficOnce()
	_ = db.Close()
}

func effectiveLogPath(cfg config.Config, fallback string) string {
	if cfg.CaddyLogPath != "" {
		return cfg.CaddyLogPath
	}
	return fallback
}

// flushTrafficLoop 周期把内存流量缓冲落库。
func flushTrafficLoop(ctx context.Context, db *store.Store) {
	ticker := time.NewTicker(trafficFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := db.FlushTraffic(); err != nil {
				log.Printf("[DB Error traffic batch] %v; batch restored for retry", err)
			}
		}
	}
}

// pruneLoop 每小时检查一次数据保留策略，删除超过保留期的历史记录。
func pruneLoop(ctx context.Context, db *store.Store, cfgMgr *config.Manager) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			retention := cfgMgr.Current().RetentionDays
			if retention < 0 {
				continue // 永久保留
			}
			cutoff := clock.Now().AddDate(0, 0, -retention).Format("2006-01-02")
			if err := db.PruneOld(cutoff); err != nil {
				log.Printf("[Prune] %v", err)
			}
		}
	}
}

// startProxyGuard 启动 SSRF 防护反代层；端口占用仅告警不致命（可选组件）。
func startProxyGuard(addr string, cfgMgr *config.Manager) {
	handler := proxyguard.New(cfgMgr.Current().BaseURL)
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	go func() {
		log.Printf("[Proxy Guard] running on %s", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[Proxy Guard] stopped: %v", err)
		}
	}()
}
