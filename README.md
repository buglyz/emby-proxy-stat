# Emby Proxy Toolbox (Go Edition) ⚡

> 基于 **Caddy 2** 与 **Go (原生静态二进制)** 构建的通用流媒体动态反向代理网关与实时流量/播放统计系统。

---

## ✨ 核心特性

- 🚀 **通用动态回源**：无需为每个上游重复配置域名，通过 `https://<gateway>/http://<upstream:port>/path` 或 `https://<gateway>/https://<upstream:port>/path` 动态代理任意 Emby / Jellyfin 实例。
- 🎬 **智能播放统计**：自动捕获视频播放握手与直通流，支持 **5 分钟客户端 IP + 媒体 ID 防抖去重**，杜绝分片重试导致的虚高计数。
- 🌐 **字节级流转流量**：精确统计流转网络下行/上行总流量，自动动态换算 `B / KB / MB / GB / TB`。
- ⚡ **极致轻量 & 高性能**：
  - 纯 Go 编写并编译为单个静态二进制文件（零外部 C 库与动态链接依赖）。
  - **内存常驻占用仅 1.3 MB ~ 3.5 MB**，CPU 消耗接近 0。
  - 采用 **内存实时原子累加 + 批量异步落库 (SQLite WAL 模式)**，彻底规避高频 I/O 损耗。
- 🤖 **GitHub Actions CI/CD**：自动构建并发布 `linux-amd64` 和 `linux-arm64` 静态二进制文件至 Releases。
- 📦 **全自动交互式安装**：脚本自动识别系统 CPU 架构、自动从 Releases 下载最新二进制文件、引导配置 Caddy 域名并自动注入/重载 Caddyfile。
- 🔐 **Web 安全访问认证**：仪表盘与数据 API 受 Session Cookie 保护，未登录无法窥视统计，媒体反代请求不受影响。
- 📱 **Telegram 每日定时播报**：每日定时自动汇总推送当日播放次数、流转流量与累计历史数据，支持手动一键测试推送。
- 🔄 **完善的日志轮转策略**：Caddy 内置日志轮转 + logrotate 系统级双重保障，支持无缝热重载与文件轮转自动感知。

---

## 🏗️ 系统架构

```
[ 客户端 Emby / Jellyfin Client ]
               │
               ▼
[ Caddy 2 反向代理网关 (auto.your-domain.com) ]
   ├── 媒体流/API 代理 ──────► [ 原始上游 Emby 实例 (动态回源) ]
   ├── 结构化访问日志 ──────► [ /var/log/caddy/<domain>.log ]
   └── 管理控制台 (/) ──────► [ emby-proxy-stat 后台服务 (127.0.0.1:8999) ]
                                    │
                                    ├─ 异步 Log Tail 解析
                                    ├─ 内存缓冲区 (5s Batch Flush)
                                    ├─ SQLite 数据库 (WAL 模式)
                                    └─ Telegram Bot 定时推送
```

---

## 📁 目录结构

```text
emby-proxy-stat/
├── .github/
│   └── workflows/
│       └── release.yml           # GitHub Actions 自动化编译与多架构 Release
├── cmd/
│   └── emby-proxy-stat/
│       ├── main.go               # Go 核心服务源码
│       └── index.html            # 嵌入式暗黑风格表格前端
├── web/
│   └── index.html                # 前端仪表盘静态文件
├── caddy/
│   └── Caddyfile                 # Caddy 动态反代与日志配置示例
├── deploy/
│   ├── emby-proxy-stat.service   # systemd 系统守护进程配置
│   ├── logrotate.caddy           # logrotate 日志轮转配置
│   └── install.sh                # 全自动交互式安装脚本 (支持 Release 下载与 Caddy 域名注入)
├── config.example.json           # 配置文件模板 (脱敏)
├── go.mod / go.sum               # Go 依赖文件
├── .gitignore
└── README.md                     # 项目说明文档
```

---

## 🚀 快速开始与一键部署

### 交互式一键部署（推荐）

直接克隆仓库并运行安装脚本（无需预装 Go 编译器，脚本会自动从 GitHub Releases 拉取对应架构的预编译二进制）：

```bash
git clone https://github.com/buglyz/emby-proxy-stat.git
cd emby-proxy-stat
sudo bash deploy/install.sh
```

**安装脚本交互流程**：
1. 🌐 **Caddy 域名配置**：输入反代域名（如 `auto.mydomain.com`），脚本自动将完整反代与日志规则写入 `/etc/caddy/Caddyfile` 并自动校验与重载 Caddy；
2. 🔐 **安全认证**：输入管理员账号与访问密码；
3. 📱 **Telegram 播报**：选择是否开启 Telegram 每日自动推送（输入 Bot Token / Chat ID / 定时推送时间）；
4. ⚡ **二进制安装**：自动探测当前 CPU 架构（amd64 / arm64）并从 GitHub Releases 自动下载部署；
5. 🔄 **服务注册**：自动配置 systemd 守护进程与 logrotate 日志轮转规则并启动。

---

### 手动构建与安装

如果您需要自行从源码编译：

```bash
# 1. 交叉编译 Linux amd64 静态二进制
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o emby-proxy-stat ./cmd/emby-proxy-stat

# 2. 拷贝配置文件
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
| `/` | `GET` | 无 | 前端表格仪表盘与登录界面 |
| `/api/login` | `POST` | 无 | 用户登录验证，下发 `auth_token` Cookie |
| `/api/logout` | `POST` | 无 | 退出登录 |
| `/api/stats` | `GET` | 需要 Cookie | 获取今日与历史播放次数及流转流量统计 |
| `/api/test-tg` | `POST` | 需要 Cookie | 立即触发一次 Telegram 播报测试 |
| `/api/health` | `GET` | 无 | 容器与服务存活探针 |

---

## 📄 开源许可证

[MIT License](LICENSE)
