#!/bin/bash
set -e

# ==========================================
# Emby Proxy Stat 一键安装/部署脚本 (Go Edition)
# ==========================================

INSTALL_DIR="/opt/emby-proxy-stat"
SYSTEMD_FILE="/etc/systemd/system/emby-proxy-stat.service"
LOGROTATE_FILE="/etc/logrotate.d/caddy"
CONFIG_FILE="${INSTALL_DIR}/config.json"

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
    read -r -p "是否重新配置账号与 Telegram 推送? [y/N]: " RECONFIGURE
    RECONFIGURE=${RECONFIGURE:-N}
else
    RECONFIGURE="y"
fi

if [[ "$RECONFIGURE" =~ ^[Yy]$ ]]; then
    echo ""
    echo "--- [1/2] 仪表盘安全认证设置 ---"
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
    echo "--- [2/2] Telegram 每日播报设置 ---"
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
  }
}
EOF
    chmod 600 "${CONFIG_FILE}"
    echo "✅ 配置文件已保存至: ${CONFIG_FILE} (权限 600)"
fi

echo "==> 3. 安装 Go 二进制程序..."
if [ -f "./emby-proxy-stat" ]; then
    cp ./emby-proxy-stat "${INSTALL_DIR}/emby-proxy-stat"
    chmod +x "${INSTALL_DIR}/emby-proxy-stat"
elif [ -f "./cmd/emby-proxy-stat/main.go" ]; then
    echo "正在从源码编译静态二进制文件..."
    if ! command -v go >/dev/null 2>&1; then
        echo "❌ 未检测到 Go 编译器，请先安装 Go 或直接提供预编译的 emby-proxy-stat 二进制文件！"
        exit 1
    fi
    CGO_ENABLED=0 go build -ldflags="-s -w" -o "${INSTALL_DIR}/emby-proxy-stat" ./cmd/emby-proxy-stat
    chmod +x "${INSTALL_DIR}/emby-proxy-stat"
else
    echo "❌ 未找到可安装的二进制文件或源码！"
    exit 1
fi

echo "==> 4. 配置并启动 systemd 服务..."
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

echo "==> 5. 配置 logrotate 日志轮转..."
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
echo "1. Caddy 反代配置参考请查看: caddy/Caddyfile"
echo "2. 访问网关域名即可体验仪表盘与反代链接生成工具！"
