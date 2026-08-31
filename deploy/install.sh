#!/bin/bash
set -e

# ==========================================
# Emby Proxy Stat 一键安装/部署脚本 (Go Edition)
# GitHub: https://github.com/buglyz/emby-proxy-stat
# ==========================================

REPO="buglyz/emby-proxy-stat"
INSTALL_DIR="/opt/emby-proxy-stat"
SYSTEMD_FILE="/etc/systemd/system/emby-proxy-stat.service"
LOGROTATE_FILE="/etc/logrotate.d/caddy"
CONFIG_FILE="${INSTALL_DIR}/config.json"
CADDYFILE="/etc/caddy/Caddyfile"
CADDY_SITES_D="/etc/caddy/sites.d"

echo "=================================================="
echo "⚡ Emby Proxy Toolbox (Go Edition) 自动化安装程序"
echo "=================================================="

# 检查 root 权限
if [ "$(id -u)" != "0" ]; then
   echo "❌ 请使用 root 权限运行此脚本 (例如: sudo bash install.sh)" 1>&2
   exit 1
fi

echo "==> 1. 创建服务与日志目录..."
mkdir -p "${INSTALL_DIR}/data"
mkdir -p /var/log/caddy
chown -R caddy:caddy /var/log/caddy 2>/dev/null || true

echo "==> 2. 配置向导 (交互式设置)..."

# 检查是否已存在配置文件
if [ -f "${CONFIG_FILE}" ]; then
    echo "⚠️ 检测到已存在配置文件: ${CONFIG_FILE}"
    read -r -p "是否重新配置网关参数与 TG 推送? [y/N]: " RECONFIGURE
    RECONFIGURE=${RECONFIGURE:-N}
else
    RECONFIGURE="y"
fi

INPUT_DOMAIN="auto.fleey.de"
CADDY_LOG_FILE="/var/log/caddy/auto.fleey.de.log"

if [[ "$RECONFIGURE" =~ ^[Yy]$ ]]; then
    echo ""
    echo "--- [1/3] Caddy 网关域名设置 ---"
    while true; do
        read -r -p "请输入反代网关绑定的域名 (例如: auto.mydomain.com): " INPUT_DOMAIN
        INPUT_DOMAIN=${INPUT_DOMAIN:-auto.fleey.de}
        if [ -n "$INPUT_DOMAIN" ]; then
            break
        fi
        echo "❌ 域名不能为空！"
    done
    CADDY_LOG_FILE="/var/log/caddy/${INPUT_DOMAIN}.log"

    echo ""
    echo "--- [2/3] 仪表盘安全认证设置 ---"
    read -r -p "请输入管理员账号 (默认: admin): " INPUT_USER
    INPUT_USER=${INPUT_USER:-admin}

    while true; do
        read -r -s -p "请输入访问密码 (必填): " INPUT_PASS
        echo ""
        if [ -n "$INPUT_PASS" ]; then
            break
        fi
        echo "❌ 密码不能为空，请重新输入！"
    done

    echo ""
    echo "--- [3/3] Telegram 每日播报设置 ---"
    read -r -p "是否开启 Telegram 每日数据播报? [Y/n]: " ENABLE_TG
    ENABLE_TG=${ENABLE_TG:-Y}

    TG_ENABLED=false
    TG_BOT_TOKEN=""
    TG_CHAT_ID=""
    TG_REPORT_TIME="23:59"

    if [[ "$ENABLE_TG" =~ ^[Yy]$ ]]; then
        TG_ENABLED=true
        while true; do
            read -r -p "请输入 Telegram Bot Token (来自 @BotFather): " TG_BOT_TOKEN
            if [ -n "$TG_BOT_TOKEN" ]; then
                break
            fi
            echo "❌ Bot Token 不能为空！"
        done

        while true; do
            read -r -p "请输入接收通知的 Chat ID (个人/群组/频道): " TG_CHAT_ID
            if [ -n "$TG_CHAT_ID" ]; then
                break
            fi
            echo "❌ Chat ID 不能为空！"
        done

        read -r -p "每日自动推送时间 (24小时制 HH:MM, 默认: 23:59): " INPUT_TIME
        TG_REPORT_TIME=${INPUT_TIME:-23:59}
    fi

    # 写入 JSON 配置文件
    cat <<EOF > "${CONFIG_FILE}"
{
  "auth": {
    "username": "${INPUT_USER}",
    "password": "${INPUT_PASS}"
  },
  "telegram": {
    "enabled": ${TG_ENABLED},
    "bot_token": "${TG_BOT_TOKEN}",
    "chat_id": "${TG_CHAT_ID}",
    "daily_report_time": "${TG_REPORT_TIME}"
  },
  "caddy_log_path": "${CADDY_LOG_FILE}"
}
EOF
    chmod 600 "${CONFIG_FILE}"
    echo "✅ 配置文件已保存至: ${CONFIG_FILE} (权限 600)"
fi

echo "==> 3. Caddy 站点配置处理..."
read -r -p "是否自动将 [${INPUT_DOMAIN}] 的反代与日志规则应用到 Caddy? [Y/n]: " APPLY_CADDY
APPLY_CADDY=${APPLY_CADDY:-Y}

if [[ "$APPLY_CADDY" =~ ^[Yy]$ ]]; then
    # 生成站点配置块
    SITE_CONF_BLOCK="
# Emby 通用反代网关 (${INPUT_DOMAIN})
https://${INPUT_DOMAIN} {
    request_body {
        max_size 500MB
    }

    log {
        output file ${CADDY_LOG_FILE} {
            roll_size 20mb
            roll_keep 7
            roll_keep_for 336h
        }
        format json
    }

    handle /api/* {
        reverse_proxy 127.0.0.1:8999
    }

    handle / {
        reverse_proxy 127.0.0.1:8999
    }

    @noSlashHttp path_regexp redir_http ^/http:/*([A-Za-z0-9.\-_:]+)$
    redir @noSlashHttp /http://{re.redir_http.1}/ 308

    @noSlashHttps path_regexp redir_https ^/https:/*([A-Za-z0-9.\-_:]+)$
    redir @noSlashHttps /https://{re.redir_https.1}/ 308

    @httpProxyWithPort path_regexp up_http_port ^/http:/*([A-Za-z0-9.\-_]+):([0-9]+)(/.*)
    handle @httpProxyWithPort {
        rewrite * {re.up_http_port.3}
        reverse_proxy {
            to {re.up_http_port.1}:{re.up_http_port.2}
            transport http {
                keepalive 30s
                keepalive_idle_conns 100
                keepalive_idle_conns_per_host 10
            }
            header_up Host {re.up_http_port.1}:{re.up_http_port.2}
            header_up X-Real-IP {remote_host}
            header_down Location ^(https?)://([^/]+)(/.*)$ https://${INPUT_DOMAIN}/\$1://\$2\$3
            header_down Location ^/(.*)$ https://${INPUT_DOMAIN}/http://{re.up_http_port.1}:{re.up_http_port.2}/\$1
            header_down Location ^([^/:][^:]*)$ https://${INPUT_DOMAIN}/http://{re.up_http_port.1}:{re.up_http_port.2}/\$1
            flush_interval -1
        }
    }

    @httpProxyNoPort path_regexp up_http_host ^/http:/*([A-Za-z0-9.\-_]+)(/.*)
    handle @httpProxyNoPort {
        rewrite * {re.up_http_host.2}
        reverse_proxy {
            to {re.up_http_host.1}:80
            transport http {
                keepalive 30s
                keepalive_idle_conns 100
                keepalive_idle_conns_per_host 10
            }
            header_up Host {re.up_http_host.1}
            header_up X-Real-IP {remote_host}
            header_down Location ^(https?)://([^/]+)(/.*)$ https://${INPUT_DOMAIN}/\$1://\$2\$3
            header_down Location ^/(.*)$ https://${INPUT_DOMAIN}/http://{re.up_http_host.1}/\$1
            header_down Location ^([^/:][^:]*)$ https://${INPUT_DOMAIN}/http://{re.up_http_host.1}/\$1
            flush_interval -1
        }
    }

    @httpsProxyWithPort path_regexp up_https_port ^/https:/*([A-Za-z0-9.\-_]+):([0-9]+)(/.*)
    handle @httpsProxyWithPort {
        rewrite * {re.up_https_port.3}
        reverse_proxy {
            to {re.up_https_port.1}:{re.up_https_port.2}
            transport http {
                tls
                keepalive 30s
                keepalive_idle_conns 100
                keepalive_idle_conns_per_host 10
            }
            header_up Host {re.up_https_port.1}:{re.up_https_port.2}
            header_up X-Real-IP {remote_host}
            header_down Location ^(https?)://([^/]+)(/.*)$ https://${INPUT_DOMAIN}/\$1://\$2\$3
            header_down Location ^/(.*)$ https://${INPUT_DOMAIN}/https://{re.up_https_port.1}:{re.up_https_port.2}/\$1
            header_down Location ^([^/:][^:]*)$ https://${INPUT_DOMAIN}/https://{re.up_https_port.1}:{re.up_https_port.2}/\$1
            flush_interval -1
        }
    }

    @httpsProxyNoPort path_regexp up_https_host ^/https:/*([A-Za-z0-9.\-_]+)(/.*)
    handle @httpsProxyNoPort {
        rewrite * {re.up_https_host.2}
        reverse_proxy {
            to {re.up_https_host.1}:443
            transport http {
                tls
                keepalive 30s
                keepalive_idle_conns 100
                keepalive_idle_conns_per_host 10
            }
            header_up Host {re.up_https_host.1}
            header_up X-Real-IP {remote_host}
            header_down Location ^(https?)://([^/]+)(/.*)$ https://${INPUT_DOMAIN}/\$1://\$2\$3
            header_down Location ^/(.*)$ https://${INPUT_DOMAIN}/https://{re.up_https_host.1}/\$1
            header_down Location ^([^/:][^:]*)$ https://${INPUT_DOMAIN}/https://{re.up_https_host.1}/\$1
            flush_interval -1
        }
    }
}
"

    if [ -d "${CADDY_SITES_D}" ]; then
        echo "${SITE_CONF_BLOCK}" > "${CADDY_SITES_D}/${INPUT_DOMAIN}.conf"
        echo "✅ 已生成 Caddy 站点配置: ${CADDY_SITES_D}/${INPUT_DOMAIN}.conf"
    fi

    if [ -f "${CADDYFILE}" ]; then
        if ! grep -q "${INPUT_DOMAIN}" "${CADDYFILE}"; then
            echo "${SITE_CONF_BLOCK}" >> "${CADDYFILE}"
            echo "✅ 已将站点配置追加至: ${CADDYFILE}"
        else
            echo "ℹ️ ${CADDYFILE} 中已存在该域名配置。"
        fi

        if command -v caddy >/dev/null 2>&1; then
            echo "正在校验 Caddy 配置..."
            if caddy validate --config "${CADDYFILE}" >/dev/null 2>&1; then
                systemctl reload caddy 2>/dev/null || systemctl restart caddy 2>/dev/null || true
                echo "✅ Caddy 已成功重载配置！"
            else
                echo "⚠️ Caddy 配置校验警告，请手动检查: caddy validate --config ${CADDYFILE}"
            fi
        fi
    fi
fi

echo "==> 4. 获取与安装 Go 二进制程序..."

# 检测系统架构
ARCH="$(uname -m)"
case "${ARCH}" in
    x86_64|amd64)
        BIN_NAME="emby-proxy-stat-linux-amd64"
        ;;
    aarch64|arm64)
        BIN_NAME="emby-proxy-stat-linux-arm64"
        ;;
    *)
        echo "⚠️ 未知或非通用架构: ${ARCH}，将尝试使用 amd64 版本或本地源码编译。"
        BIN_NAME="emby-proxy-stat-linux-amd64"
        ;;
esac

INSTALLED_BIN=false

# 优先级 1: 本地已有同目录编译好的二进制
if [ -f "./emby-proxy-stat" ]; then
    echo "使用本地已有的二进制文件: ./emby-proxy-stat"
    cp ./emby-proxy-stat "${INSTALL_DIR}/emby-proxy-stat"
    chmod +x "${INSTALL_DIR}/emby-proxy-stat"
    INSTALLED_BIN=true
elif [ -f "./${BIN_NAME}" ]; then
    echo "使用本地已有的架构二进制文件: ./${BIN_NAME}"
    cp "./${BIN_NAME}" "${INSTALL_DIR}/emby-proxy-stat"
    chmod +x "${INSTALL_DIR}/emby-proxy-stat"
    INSTALLED_BIN=true
fi

# 优先级 2: 自动从 GitHub Release 下载预构建二进制
if [ "${INSTALLED_BIN}" = false ]; then
    DOWNLOAD_URL="https://github.com/${REPO}/releases/latest/download/${BIN_NAME}"
    echo "正在从 GitHub Releases 下载最新的预构建二进制 (${BIN_NAME})..."
    echo "下载地址: ${DOWNLOAD_URL}"
    
    if command -v curl >/dev/null 2>&1; then
        if curl -sSL -f -o "${INSTALL_DIR}/emby-proxy-stat" "${DOWNLOAD_URL}"; then
            chmod +x "${INSTALL_DIR}/emby-proxy-stat"
            INSTALLED_BIN=true
            echo "✅ 二进制文件下载成功！"
        fi
    elif command -v wget >/dev/null 2>&1; then
        if wget -q -O "${INSTALL_DIR}/emby-proxy-stat" "${DOWNLOAD_URL}"; then
            chmod +x "${INSTALL_DIR}/emby-proxy-stat"
            INSTALLED_BIN=true
            echo "✅ 二进制文件下载成功！"
        fi
    fi
fi

# 优先级 3: 尝试从本地源码构建 (如果前两步未成功)
if [ "${INSTALLED_BIN}" = false ]; then
    if [ -f "./cmd/emby-proxy-stat/main.go" ] && command -v go >/dev/null 2>&1; then
        echo "正在从源码编译静态二进制文件..."
        CGO_ENABLED=0 go build -ldflags="-s -w" -o "${INSTALL_DIR}/emby-proxy-stat" ./cmd/emby-proxy-stat
        chmod +x "${INSTALL_DIR}/emby-proxy-stat"
        INSTALLED_BIN=true
        echo "✅ 本地源码编译成功！"
    fi
fi

if [ "${INSTALLED_BIN}" = false ]; then
    echo "❌ 无法获取或构建二进制文件！请检查网络连接或手动下载 release 后放入目录再执行安装。"
    exit 1
fi

echo "==> 5. 配置并启动 systemd 服务..."
if [ -f "./deploy/emby-proxy-stat.service" ]; then
    cp ./deploy/emby-proxy-stat.service "${SYSTEMD_FILE}"
elif [ ! -f "${SYSTEMD_FILE}" ]; then
    cat <<EOF > "${SYSTEMD_FILE}"
[Unit]
Description=Emby Proxy Statistics Service (Go Edition)
After=network.target caddy.service

[Service]
Type=simple
User=root
WorkingDirectory=${INSTALL_DIR}
ExecStart=${INSTALL_DIR}/emby-proxy-stat
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
fi

systemctl daemon-reload
systemctl enable --now emby-proxy-stat
systemctl restart emby-proxy-stat

echo "==> 6. 配置 logrotate 日志轮转..."
if [ -f "./deploy/logrotate.caddy" ]; then
    cp ./deploy/logrotate.caddy "${LOGROTATE_FILE}"
    chmod 644 "${LOGROTATE_FILE}"
fi

echo ""
echo "=================================================="
echo "🎉 安装完成！服务运行状态："
systemctl status emby-proxy-stat --no-pager
echo "=================================================="
echo "提示："
echo "1. 访问 https://${INPUT_DOMAIN}/ 即可进入统计仪表盘与链接生成器！"
echo "2. 配置文件路径: ${CONFIG_FILE}"
