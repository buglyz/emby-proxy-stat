// Package store 封装 SQLite 持久化：播放事件、按日流量与统计查询。
//
// 并发模型：dbMu 串行化所有数据库访问（modernc.org/sqlite 单写多读）；
// trafficMu 保护内存流量缓冲。Stats 全程持有 dbMu 后才快照缓冲，
// 保证同一批字节不会在"内存 + 数据库"中被重复计入（刷盘事务同样需要 dbMu）。
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// DebounceWindow 是同一客户端+上游+媒体的有效播放去重窗口。
const DebounceWindow = 30 * time.Minute

// Store 是统计存储的统一入口。
type Store struct {
	db   *sql.DB
	dbMu chan struct{} // 用带缓冲 channel 代替 Mutex，便于 Select 超时扩展

	trafficMu     sync.Mutex
	traffic       map[string]*TrafficEntry           // 全局按日流量（未落库部分）
	trafficDetail map[trafficDetailKey]*TrafficEntry // 客户端×节点明细（未落库部分）

	recentMu       sync.Mutex
	recentPlays    map[string]int64
	debounceWindow time.Duration
}

// trafficDetailKey 标识某天某客户端到某节点的流量。
type trafficDetailKey struct {
	date   string
	client string
	target string
}

// TrafficEntry 是某天的流量聚合。
type TrafficEntry struct {
	Bytes    int64
	Requests int64
}

// Open 打开（必要时创建）数据库并初始化表结构。
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{
		db:             db,
		dbMu:           make(chan struct{}, 1),
		traffic:        make(map[string]*TrafficEntry),
		trafficDetail:  make(map[trafficDetailKey]*TrafficEntry),
		recentPlays:    make(map[string]int64),
		debounceWindow: DebounceWindow,
	}, nil
}

func migrate(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS play_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		played_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		play_date TEXT,
		client_ip TEXT,
		target_host TEXT,
		item_id TEXT,
		uri TEXT,
		device_name TEXT DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_play_date ON play_events(play_date);
	CREATE INDEX IF NOT EXISTS idx_played_at ON play_events(played_at);
	CREATE TABLE IF NOT EXISTS daily_traffic (
		date TEXT PRIMARY KEY,
		bytes INTEGER DEFAULT 0,
		requests INTEGER DEFAULT 0,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE IF NOT EXISTS traffic_by_client (
		date TEXT,
		client_ip TEXT,
		target_host TEXT,
		bytes INTEGER DEFAULT 0,
		requests INTEGER DEFAULT 0,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (date, client_ip, target_host)
	);
	CREATE INDEX IF NOT EXISTS idx_tbc_date ON traffic_by_client(date);
	`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate schema: %w", err)
	}
	// 旧库平滑迁移：补 device_name 列
	var colCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('play_events') WHERE name = 'device_name'").Scan(&colCount)
	if colCount == 0 {
		_, _ = db.Exec("ALTER TABLE play_events ADD COLUMN device_name TEXT DEFAULT ''")
	}
	return nil
}

// Close 释放数据库连接。
func (s *Store) Close() error { return s.db.Close() }

// withDB 以独占方式持有数据库锁执行 fn。
func (s *Store) withDB(fn func(db *sql.DB) error) error {
	s.dbMu <- struct{}{}
	defer func() { <-s.dbMu }()
	return fn(s.db)
}

// FormatBytes 将字节数格式化为人类可读字符串（B/KB/MB/GB/TB）。
func FormatBytes(b int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
		tb = 1024 * gb
	)
	fb := float64(b)
	switch {
	case b >= tb:
		return fmt.Sprintf("%.2f TB", fb/float64(tb))
	case b >= gb:
		return fmt.Sprintf("%.2f GB", fb/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.2f MB", fb/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.2f KB", fb/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
