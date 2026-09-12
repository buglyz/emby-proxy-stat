# Emby Proxy Toolbox (Go Edition) ⚡

> 基于 **Caddy 2** 与 **Go（原生零依赖静态二进制）** 构建的通用流媒体动态反向代理网关与实时播放/流量统计系统。

---

## ✨ 核心特性

- 🚀 **通用动态回源（自带 SSRF 防护）**：通过 `https://<gateway>/http://<upstream:port>/路径` 或 `https://<gateway>/https://<upstream:port>/路径` 动态反代任意 Emby / Jellyfin 实例。动态回源统一经内置 **proxyguard**（127.0.0.1:8998）校验：拒绝环回/私网/链路本地/CGNAT 上游，DNS 解析结果钉扎拨号（防重绑定绕过），60s 解析缓存，301/302 Location 自动改写回网关格式。
- 🎬 **精准心跳播放统计（方案 B）**：
  - 仅当客户端发送 `/Sessions/Playing/Progress` 播放心跳时计入有效播放，详情页（`PlaybackInfo`）预加载不误判；
  - 心跳与媒体流按 **设备名 × 上游** 关联（设备名取自 Emby 鉴权头，不随移动网络 NAT 的 IP 漂移变化），无设备名时回退 IP 键；
  - **滑动 30 分钟防抖窗口**：连续观看同一内容只计一次，停止超过窗口后再看才计新的一次，长视频不再被重复计数。
- 🌐 **字节级流量统计**：按访问日志响应字节数统计网关下行流量（不含请求体与协议头开销），自动换算 `B / KB / MB / GB / TB`，支持按客户端 / 按目标节点的分布明细。
- 📊 **WebUI 仪表盘**（单文件、零外部依赖、原生 SVG 图表）：
  - 实时观看会话（2 分钟内有媒体流请求即视为活跃）；
  - 今日流量分布（按客户端 / 按节点）；
  - 最近 30 天播放与流量趋势（双轴、十字准星交互）；
  - 今日播放分布环形图（按设备 / 按节点切换）；
  - 骨架屏加载、数字滚动动画、优雅空状态、移动端 2×2 卡片自适应、iOS Safari 优化（16px 输入框防缩放、safe-area 底部安全区）、`:focus-visible` 与 `aria-label` 无障碍支持。
- 🔔 **富文本 Telegram 每日运营日报**：定时推送今日有效播放、流转流量、独立设备数、覆盖节点数与历史全量汇总，支持 Web 端一键触发连通测试。
- 🗄️ **数据保留策略**：`retention_days` 自动清理过期播放与流量记录（默认 365 天，负数永久保留）。
- 🛡️ **生产级健壮性**：
  - **零 CGO**：基于纯 Go 的 `modernc.org/sqlite`，任意 Linux 即插即用；
  - 常驻 Goroutine Panic 自愈；无感日志轮转追踪（首次启动 Seek 到末尾，轮转/截断重开从文件头读取）；
  - 内存防泄漏：活跃流、防抖缓存、过期会话周期回收；
  - HTTP 防护：1MB Body 限额、5s 请求头超时（防 Slowloris）；
  - **SIGHUP 热重载配置**；**SIGTERM 优雅退出**并冲刷流量缓冲，重启不丢数据；
  - 客户端 IP 取 **X-Forwarded-For 可信链尾**（Caddy 追加段），伪造链首无法绕过登录限流或污染统计。
- ⚡ **极致轻量**：常驻内存 ~3MB，CPU 趋近 0；内存实时累加 + 5s 批量落库（SQLite WAL）。

---

## 🏗️ 系统架构

```text
[ 客户端 Emby / Jellyfin Client ]
               │
               ▼
[ Caddy 2 反向代理网关 (auto.your-domain.com) ]
   ├── /http(s)://上游/路径 ──► [ proxyguard :8998 ] ──► [ 公网上游 Emby 实例 ]
   │                             （SSRF 校验 + DNS 钉扎 + Location 改写）
   ├── 结构化访问日志 ────────► [ /var/log/caddy/<domain>.log ]
   └── / 与 /api/* ──────────► [ emby-proxy-stat :8999 ]
                                    ├─ 异步 Log Tail（Progress 心跳过滤）
                                    ├─ 内存缓冲（5s 批量写入 SQLite WAL）
                                    ├─ Telegram 运营日报
                                    └─ 数据保留清理（每日）
```

## 📁 目录结构

```text
emby-proxy-stat/
├── .github/workflows/release.yml   # GitHub Actions 多架构构建与发布
├── cmd/emby-proxy-stat/main.go     # 入口：依赖组装、信号处理（SIGHUP/SIGTERM）
├── internal/
│   ├── auth/                       # PBKDF2 密码校验、会话管理、登录限流
│   ├── caddylog/                   # 日志 tail、轮转检测、播放心跳识别流水线
│   ├── clock/                      # 业务时区（Asia/Shanghai）与日报到期判断
│   ├── config/                     # 配置加载/校验/热重载
│   ├── netutil/                    # 客户端 IP 归一化（XFF 可信链尾提取）
│   ├── notify/                     # Telegram 发送与每日运营日报
│   ├── proxyguard/                 # SSRF 防护动态反代（:8998）
│   ├── store/                      # SQLite(WAL)：播放事件、流量缓冲、统计查询
│   └── web/                        # 仪表盘 HTTP 服务与内嵌前端（index.html）
├── caddy/Caddyfile                 # Caddy 反代与 proxyguard 接线配置范本
├── deploy/
│   ├── emby-proxy-stat.service     # systemd 守护进程配置
│   ├── logrotate.caddy             # 日志轮转配置（copytruncate 安全模式）
│   └── install.sh                  # 全自动一键安装脚本
├── config.example.json             # 配置文件模板（脱敏）
└── go.mod / go.sum
```

---

## 🚀 快速开始与一键部署

```bash
git clone https://github.com/buglyz/emby-proxy-stat.git
cd emby-proxy-stat
sudo bash deploy/install.sh
```

安装脚本自动完成：Caddy 域名配置与重载 → 仪表盘管理员账号（PBKDF2 哈希）→ Telegram 播报（可选）→ 探测架构下载预编译二进制 → systemd 服务托管。

### 手动构建

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o emby-proxy-stat ./cmd/emby-proxy-stat

# 测试（含竞态检测）
go test -race ./...

# 生成密码哈希
echo 'your-password' | ./emby-proxy-stat -password-hash
```

---

## 📊 接口说明

| 接口 | 方法 | 鉴权 | 说明 |
| :--- | :--- | :--- | :--- |
| `/` | GET | 无 | 前端仪表盘（登录/看板单页） |
| `/api/login` | POST | 无 | 登录，签发 30 天 HttpOnly Cookie（限流 5 次/10 分钟） |
| `/api/logout` | POST | 无 | 退出登录 |
| `/api/stats` | GET | Cookie | 今日与历史播放、流量、设备数、节点数 |
| `/api/clients` | GET | Cookie | 今日播放明细（设备×IP×节点聚合） |
| `/api/sessions` | GET | Cookie | 实时观看会话 |
| `/api/traffic-breakdown` | GET | Cookie | 今日流量按客户端/按节点分布 |
| `/api/trend?days=30` | GET | Cookie | 最近 N 天播放与流量趋势（上限 365） |
| `/api/test-tg` | POST | Cookie | 触发一次 Telegram 测试推送 |
| `/api/health` | GET | 无 | 存活探针，返回 `{"status":"ok","engine":"go"}` |

### `/api/stats` 响应示例

```json
{
  "today_plays": 5,
  "total_plays": 128,
  "today_bytes": 1856942000,
  "total_bytes": 4269106199,
  "today_traffic_fmt": "1.73 GB",
  "total_traffic_fmt": "3.98 GB",
  "today_clients": 2,
  "total_clients": 8,
  "today_hosts": 1,
  "total_hosts": 4,
  "date": "2026-09-07",
  "status": "online"
}
```

---

## 📄 统计口径

- **流量**：Caddy 访问日志的响应字节数（下行），4xx/5xx 不计入；分布明细自 `traffic_by_client` 表上线起累积。
- **播放**：仅 `Sessions/Playing/Progress` 心跳计数；滑动 30 分钟窗口内同一 IP×上游×内容只计一次。

## 📄 开源许可证

[MIT License](LICENSE)
