# Emby Proxy Toolbox (Go Edition) ⚡

> 基于 **Caddy 2** 与 **Go (原生零依赖静态二进制)** 构建的通用流媒体动态反向代理网关与实时流量/播放统计系统。

---

## ✨ 核心特性

- 🚀 **通用动态回源**：无需为每个上游重复配置域名，通过 `https://<gateway>/http://<upstream:port>/path` 或 `https://<gateway>/https://<upstream:port>/path` 动态反向代理任意 Emby / Jellyfin 实例，支持 301/302 重定向 Location 头自动改写。
- 🎬 **精准心跳播放统计（方案 B）**：
  - **杜绝虚高误判**：彻底排除详情页（`PlaybackInfo`）预加载造成的误判；
  - **基于心跳精准捕获**：仅在客户端发送 `/Sessions/Playing/Progress` 播放心跳时计入有效播放（表明客户端已持续稳定播放超过 5 秒）；
  - **媒体流精准绑定**：自动关联当前客户端的媒体流请求，精确记录真实 `Item_ID`、客户端 IP 与目标上游域名；
  - **长效防抖去重**：同一客户端 + 同一上游 + 同一视频启用 **30 分钟防抖**，单次观影中的快进、分段 Range 不再重复计数。
- 🌐 **字节级流转流量统计**：精确统计网关流转的网络下行与上行总流量，自动动态换算 `B / KB / MB / GB / TB`。
- 📱 **响应式现代移动端 WebUI**：
  - **大屏（桌面端）**：呈现高信息密度的专业指标表格视图；
  - **移动端（竖屏/小屏）**：自适应切换为 **2×2 响应式大数字指标卡片（Grid Cards）**，播放与流量一目了然；
  - **iOS Safari 体验优化**：表单输入框严格规范 `16px` 字号，彻底杜绝点击输入框时页面被浏览器自动放大拉伸的痛点；全面屏底部安全区（`safe-area-inset-bottom`）贴合。
- 🔔 **富文本 Telegram 每日运营日报**：
  - 每日定时汇总推送精美格式化排版卡片，支持展示**今日有效播放**、**流转流量**、**独立设备数**、**覆盖上游节点数**与历史全量汇总；
  - 包含控制台面板直达链接与格式化时间戳，支持 Web 端一键触发联通测试推送。
- 🛡️ **生产级高健壮性与安全设计**：
  - **零外部 C 库依赖 (Zero-CGO)**：基于纯 Go 实现的 `modernc.org/sqlite`，可在任何现代 Linux 环境即插即用；
  - **常驻 Goroutine Panic 自愈**：日志监听、流量批量刷盘与 TG 定时任务均内置 Panic Recover 与自愈重启机制；
  - **无感日志滚动追踪**：首次启动 Seek 到末尾，轮转与截断重开后从文件头读取，绝不丢日志；
  - **内存防泄漏治理**：内置周期性定时清理引擎，自动回收超时的活跃流字典、防抖缓存及过期 Session；
  - **HTTP 攻击防护**：具备 1MB Body 读取限额与 5 秒请求头超时限制（防御 Slowloris 慢速连接攻击）。
- ⚡ **极致轻量 & 高性能**：
  - 内存常驻占用仅 **1.5 MB ~ 3.5 MB**，CPU 消耗常年趋近于 0；
  - 采用 **内存实时原子累加 + 批量异步落库 (SQLite WAL 模式)**，消除高频磁盘 I/O 开销。

---

## 🏗️ 系统架构

```text
[ 客户端 Emby / Jellyfin Client ]
               │
               ▼
[ Caddy 2 反向代理网关 (auto.your-domain.com) ]
   ├── 媒体流/API 代理 ──────► [ 原始上游 Emby 实例 (动态回源) ]
   ├── 结构化访问日志 ──────► [ /var/log/caddy/<domain>.log ]
   └── 管理控制台 (/) ──────► [ emby-proxy-stat 后台服务 (127.0.0.1:8999) ]
                                    │
                                    ├─ 异步 Log Tail 监听 (Progress 心跳过滤)
                                    ├─ 内存缓冲区 (5s 批量写入 + 内存防泄漏回收)
                                    ├─ SQLite 数据库 (WAL 模式)
                                    └─ Telegram 运营日报定时推送
```

---

## 📁 目录结构

```text
emby-proxy-stat/
├── .github/
│   └── workflows/
│       └── release.yml           # GitHub Actions 自动化多架构跨平台构建
├── cmd/
│   └── emby-proxy-stat/
│       ├── main.go               # Go 核心服务源码 (嵌入式 WebUI、API、LogTailer)
│       └── index.html            # 仪表盘前端界面 (支持桌面端表格与移动端 2x2 卡片)
├── web/
│   └── index.html                # 前端静态源码备份
├── caddy/
│   └── Caddyfile                 # Caddy 动态反向代理与日志配置范本
├── deploy/
│   ├── emby-proxy-stat.service   # systemd 守护进程配置文件
│   ├── logrotate.caddy           # logrotate 日志轮转配置 (copytruncate 安全模式)
│   └── install.sh                # 全自动一键安装脚本
├── config.example.json           # 配置文件模板 (脱敏)
├── go.mod / go.sum               # Go 模块与依赖定义
├── .gitignore
└── README.md                     # 项目说明文档
```

---

## 🚀 快速开始与一键部署

### 交互式一键部署（推荐）

直接克隆仓库并运行安装脚本（脚本会自动从 GitHub Releases 下载预编译好的单一静态二进制文件，无需预装 Go 编译器）：

```bash
git clone https://github.com/buglyz/emby-proxy-stat.git
cd emby-proxy-stat
sudo bash deploy/install.sh
```

**安装脚本自动化流程**：
1. 🌐 **Caddy 域名配置**：输入反代域名（如 `auto.mydomain.com`），脚本自动写入完整反代与日志规则并重载 Caddy；
2. 🔐 **安全认证**：设置 Web 仪表盘管理员账号与访问密码；
3. 📱 **Telegram 播报**：选择是否开启 Telegram 每日自动推送（输入 Bot Token / Chat ID / 定时推送时间）；
4. ⚡ **二进制部署**：自动探测 CPU 架构（amd64 / arm64）并下载匹配的最新二进制；
5. 🔄 **服务托管**：配置并启动 systemd 系统服务。

---

### 手动构建与运行

如果您需要自行从源码编译构建：

```bash
# 1. 编译 Linux amd64 纯静态二进制
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o emby-proxy-stat ./cmd/emby-proxy-stat

# 2. 准备配置文件
mkdir -p /opt/emby-proxy-stat/data
cp config.example.json /opt/emby-proxy-stat/config.json
chmod 600 /opt/emby-proxy-stat/config.json

# 3. 注册并启动 systemd 服务
cp deploy/emby-proxy-stat.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now emby-proxy-stat
```

---

## 📊 接口说明

| 接口 | 方法 | 鉴权要求 | 说明 |
| :--- | :--- | :--- | :--- |
| `/` | `GET` | 无 | 前端仪表盘与登录界面（支持响应式自适应布局） |
| `/api/login` | `POST` | 无 | 用户登录验证，下发 `auth_token` Cookie（带 1MB 限额保护） |
| `/api/logout` | `POST` | 无 | 退出登录并作废令牌 |
| `/api/stats` | `GET` | 需要 Cookie | 获取今日与历史播放次数、流转流量、设备数与上游节点数 |
| `/api/test-tg` | `POST` | 需要 Cookie | 立即触发一次 Telegram 运营富文本测试推送 |
| `/api/health` | `GET` | 无 | 存活探针，返回 `{"status":"ok","engine":"go"}` |

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

## 📄 开源许可证

[MIT License](LICENSE)
