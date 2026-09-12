#!/usr/bin/env bash
set -euo pipefail

# ==========================================================
# Emby Proxy Toolbox (Go Edition) 交互式安装/更新脚本
# GitHub: https://github.com/buglyz/emby-proxy-stat
#
# 用法:
#   sudo bash install.sh             # 自动判断: 全新安装或弹出管理菜单
#   sudo bash install.sh update      # 仅更新程序到最新 Release
#   sudo bash install.sh reconfigure # 重新运行配置向导
#   sudo bash install.sh caddy       # 重新追加/修复 Caddy 站点配置
#   sudo bash install.sh status      # 查看服务状态
# ==========================================================

REPO="buglyz/emby-proxy-stat"
INSTALL_DIR="/opt/emby-proxy-stat"
SERVICE_NAME="emby-proxy-stat"
SERVICE_USER="emby-proxy-stat"
SYSTEMD_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
CONFIG_FILE="${INSTALL_DIR}/config.json"
BIN_FILE="${INSTALL_DIR}/emby-proxy-stat"
CADDYFILE="/etc/caddy/Caddyfile"
API_PORT="8999"
GUARD_PORT="8998"

# 向导收集的输入（全局）
INPUT_DOMAIN=""
INPUT_USER="admin"
INPUT_PASS=""
TG_ENABLED="false"
TG_BOT_TOKEN=""
TG_CHAT_ID=""
TG_REPORT_TIME="23:59"
CADDY_LOG_FILE=""
BASE_URL=""
RETENTION_DAYS="365"

info() { echo "==> ${*}"; }
ok() { echo "✅ ${*}"; }
warn() { echo "⚠️  ${*}"; }
die() { echo "❌ ${*}" 1>&2; exit 1; }

require_root() {
    if [[ "$(id -u)" != "0" ]]; then
        die "请使用 root 权限运行此脚本 (例如: sudo bash install.sh)"
    fi
}

ask() {
    # ask <提示> <结果变量名>
    local prompt="${1}" var="${2}" value=""
    read -r -p "${prompt}: " value
    printf -v "${var}" '%s' "${value}"
}

ask_default() {
    # ask_default <提示> <默认值> <结果变量名>
    local prompt="${1}" default="${2}" var="${3}" value=""
    read -r -p "${prompt} [${default}]: " value
    printf -v "${var}" '%s' "${value:-${default}}"
}

ask_secret() {
    # ask_secret <提示> <结果变量名>
    local prompt="${1}" var="${2}" value=""
    read -r -s -p "${prompt}: " value
    echo ""
    printf -v "${var}" '%s' "${value}"
}

installed() {
    [[ -f "${SYSTEMD_FILE}" ]] || [[ -f "${BIN_FILE}" ]]
}

download() {
    # download <URL> <目标文件>
    local url="${1}" target="${2}"
    if command -v curl >/dev/null 2>&1; then
        curl -sSL -f --retry 3 -o "${target}" "${url}"
    elif command -v wget >/dev/null 2>&1; then
        wget -q -O "${target}" "${url}"
    else
        return 1
    fi
}

release_binary_name() {
    case "$(uname -m)" in
        x86_64|amd64) echo "emby-proxy-stat-linux-amd64" ;;
        aarch64|arm64) echo "emby-proxy-stat-linux-arm64" ;;
        *) echo "" ;;
    esac
}

# acquire_binary <目标路径>
# 优先级: 脚本同目录现成二进制 → GitHub Release 下载 → 本地源码编译
acquire_binary() {
    local target="${1}"
    local bin_name script_dir candidate

    bin_name="$(release_binary_name)"
    if [[ -z "${bin_name}" ]]; then
        warn "未知架构 $(uname -m)，跳过预编译下载，将尝试源码编译"
    fi

    script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    for candidate in "${script_dir}/emby-proxy-stat" "${script_dir}/${bin_name}"; do
        if [[ -x "${candidate}" ]]; then
            info "使用本地现成二进制: ${candidate}"
            cp "${candidate}" "${target}"
            chmod 755 "${target}"
            return 0
        fi
    done

    if [[ -n "${bin_name}" ]]; then
        local url="https://github.com/${REPO}/releases/latest/download/${bin_name}"
        info "从 GitHub Releases 下载最新二进制 (${bin_name})..."
        if download "${url}" "${target}"; then
            chmod 755 "${target}"
            return 0
        fi
        warn "下载失败，将尝试本地源码编译"
        rm -f "${target}"
    fi

    if command -v go >/dev/null 2>&1 && [[ -f "${script_dir}/cmd/emby-proxy-stat/main.go" ]]; then
        info "从本地源码编译静态二进制..."
        (cd "${script_dir}" && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "${target}" ./cmd/emby-proxy-stat)
        chmod 755 "${target}"
        return 0
    fi

    return 1
}

# smoke_test_binary <路径>: 执行 -h 自检，退出码 0(usage) 或 2(参数错误) 均视为可运行
smoke_test_binary() {
    local target="${1}" rc="0"
    [[ -s "${target}" ]] || return 1
    "${target}" -h >/dev/null 2>&1 || rc=$?
    [[ "${rc}" -le 2 ]]
}

# ---------------- 配置向导 ----------------

wizard() {
    echo ""
    echo "------------- 配置向导 -------------"

    while true; do
        ask "请输入网关绑定的域名 (例如: auto.mydomain.com)" INPUT_DOMAIN
        if [[ "${INPUT_DOMAIN}" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ ]]; then
            break
        fi
        echo "❌ 域名不能为空，且只允许字母、数字、点和短横线。"
    done
    CADDY_LOG_FILE="/var/log/caddy/${INPUT_DOMAIN}.log"

    echo ""
    echo "--- 仪表盘安全认证 ---"
    while true; do
        ask_default "管理员账号" "admin" INPUT_USER
        if [[ "${INPUT_USER}" =~ ^[A-Za-z0-9._-]{1,64}$ ]]; then
            break
        fi
        echo "❌ 账号只能包含字母、数字、点、下划线和短横线。"
    done

    while true; do
        ask_secret "请输入访问密码 (输入不回显)" INPUT_PASS
        if [[ -z "${INPUT_PASS}" ]]; then
            echo "❌ 密码不能为空。"
            continue
        fi
        if [[ ${#INPUT_PASS} -lt 8 ]]; then
            warn "密码少于 8 位，建议使用更长的密码。"
        fi
        local confirm=""
        ask_secret "请再输入一次以确认" confirm
        if [[ "${INPUT_PASS}" = "${confirm}" ]]; then
            break
        fi
        echo "❌ 两次输入不一致，请重新设置。"
    done

    echo ""
    echo "--- Telegram 每日播报 (可选) ---"
    local enable_tg="N"
    ask_default "是否开启 Telegram 每日数据播报? (y/N)" "N" enable_tg
    if [[ "${enable_tg}" =~ ^[Yy]$ ]]; then
        TG_ENABLED="true"
        while true; do
            ask "请输入 Telegram Bot Token (来自 @BotFather)" TG_BOT_TOKEN
            if [[ "${TG_BOT_TOKEN}" =~ ^[0-9]+:[A-Za-z0-9_-]+$ ]]; then
                break
            fi
            echo "❌ Bot Token 格式无效 (应为 数字:字母数字组合)。"
        done
        while true; do
            ask "请输入接收通知的 Chat ID (个人/群组/频道)" TG_CHAT_ID
            if [[ "${TG_CHAT_ID}" =~ ^-?[0-9]+$ || "${TG_CHAT_ID}" =~ ^@[A-Za-z0-9_]{5,}$ ]]; then
                break
            fi
            echo "❌ Chat ID 格式无效。"
        done
        while true; do
            ask_default "每日自动推送时间 (HH:MM)" "23:59" TG_REPORT_TIME
            if [[ "${TG_REPORT_TIME}" =~ ^([01][0-9]|2[0-3]):[0-5][0-9]$ ]]; then
                break
            fi
            echo "❌ 时间必须是 HH:MM 格式。"
        done
    else
        TG_ENABLED="false"
        TG_BOT_TOKEN=""
        TG_CHAT_ID=""
    fi

    echo ""
    echo "--- 高级选项 ---"
    ask_default "Caddy 访问日志路径" "${CADDY_LOG_FILE}" CADDY_LOG_FILE
    ask_default "对外访问地址 base_url" "https://${INPUT_DOMAIN}" BASE_URL
    while [[ ! "${BASE_URL}" =~ ^https?://[^/]+ ]]; do
        echo "❌ base_url 必须以 http:// 或 https:// 开头。"
        ask_default "对外访问地址 base_url" "https://${INPUT_DOMAIN}" BASE_URL
    done
    BASE_URL="${BASE_URL%/}"
    while true; do
        ask_default "数据保留天数 (负数=永久保留)" "365" RETENTION_DAYS
        if [[ "${RETENTION_DAYS}" =~ ^-?[0-9]+$ ]]; then
            break
        fi
        echo "❌ 保留天数必须是整数。"
    done
    echo "------------------------------------"
}

write_config() {
    local hash
    info "生成密码哈希 (PBKDF2)..."
    hash="$(printf '%s' "${INPUT_PASS}" | "${BIN_FILE}" -password-hash)"
    if [[ -z "${hash}" ]]; then
        die "密码哈希生成失败，拒绝写入不安全的明文配置"
    fi

    umask 077
    cat > "${CONFIG_FILE}" <<EOF
{
  "auth": {
    "username": "${INPUT_USER}",
    "password_hash": "${hash}"
  },
  "telegram": {
    "enabled": ${TG_ENABLED},
    "bot_token": "${TG_BOT_TOKEN}",
    "chat_id": "${TG_CHAT_ID}",
    "daily_report_time": "${TG_REPORT_TIME}"
  },
  "caddy_log_path": "${CADDY_LOG_FILE}",
  "base_url": "${BASE_URL}",
  "retention_days": ${RETENTION_DAYS}
}
EOF
    unset INPUT_PASS
    chown root:"${SERVICE_USER}" "${CONFIG_FILE}"
    chmod 640 "${CONFIG_FILE}"
    ok "配置已写入: ${CONFIG_FILE}"
}

# ---------------- Caddy ----------------

caddy_installed() { command -v caddy >/dev/null 2>&1; }

install_caddy_prompt() {
    if caddy_installed; then
        ok "已检测到 Caddy: $(caddy version 2>/dev/null | head -1)"
        return 0
    fi
    warn "未检测到 Caddy。"
    local choice="N"
    ask_default "是否尝试通过系统包管理器自动安装 Caddy? (y/N)" "N" choice
    if [[ ! "${choice}" =~ ^[Yy]$ ]]; then
        warn "跳过 Caddy 安装。稍后可手动安装后运行: sudo bash $0 caddy"
        return 1
    fi
    if command -v apt-get >/dev/null 2>&1; then
        apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl
        curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
        curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | tee /etc/apt/sources.list.d/caddy-stable.list > /dev/null
        apt-get update -y
        apt-get install -y caddy
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y dnf-plugins-core
        dnf copr enable -y @caddy/caddy
        dnf install -y caddy
    else
        warn "不支持的包管理器，请参考 https://caddyserver.com/docs/install 手动安装。"
        return 1
    fi
    caddy_installed
}

gen_site_block() {
    cat <<EOF

# ------------------------------------------------------
# Emby 通用反代网关 (${INPUT_DOMAIN}) — 由 emby-proxy-stat 安装脚本追加
# 动态回源统一经 127.0.0.1:${GUARD_PORT} proxyguard 校验:
# 拒绝环回/私网/链路本地/CGNAT 上游, DNS 钉扎, Location 改写
# ------------------------------------------------------
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
        reverse_proxy 127.0.0.1:${API_PORT}
    }

    handle / {
        reverse_proxy 127.0.0.1:${API_PORT}
    }

    @noSlashHttp path_regexp redir_http ^/http:/*([A-Za-z0-9.\-_:]+)\$
    redir @noSlashHttp /http://{re.redir_http.1}/ 308

    @noSlashHttps path_regexp redir_https ^/https:/*([A-Za-z0-9.\-_]+)\$
    redir @noSlashHttps /https://{re.redir_https.1}/ 308

    @dynamicProxy path_regexp dynamic_proxy ^/(http|https):/*([A-Za-z0-9.\-_]+)(:[0-9]+)?(/.*)
    handle @dynamicProxy {
        reverse_proxy 127.0.0.1:${GUARD_PORT} {
            flush_interval -1
        }
    }
}
EOF
}

apply_caddy_site() {
    if ! caddy_installed; then
        warn "Caddy 未安装，跳过站点配置。安装 Caddy 后运行: sudo bash $0 caddy"
        return 1
    fi

    # caddyctl 托管的环境不允许直接改 Caddyfile (会被 caddyctl apply 覆盖)
    if command -v caddyctl >/dev/null 2>&1; then
        warn "检测到 caddyctl 托管环境，直接追加会被 caddyctl apply 覆盖。"
        warn "请改用: caddyctl add-gateway ${INPUT_DOMAIN} --unsafe-open-proxy (或先配置 allow 列表)"
        return 1
    fi

    mkdir -p "$(dirname "${CADDY_LOG_FILE}")"
    chown -R caddy:caddy "$(dirname "${CADDY_LOG_FILE}")" 2>/dev/null || true

    if [[ -f "${CADDYFILE}" ]] && grep -qF "https://${INPUT_DOMAIN} {" "${CADDYFILE}"; then
        ok "${CADDYFILE} 中已存在 ${INPUT_DOMAIN} 的站点配置，跳过追加。"
        return 0
    fi

    # 追加前备份，validate 失败即整体回滚
    local backup=""
    if [[ -f "${CADDYFILE}" ]]; then
        backup="${CADDYFILE}.bak.$(date +%Y%m%d%H%M%S)"
        cp -a "${CADDYFILE}" "${backup}"
        info "已备份现有配置: ${backup}"
        gen_site_block >> "${CADDYFILE}"
    else
        gen_site_block > "${CADDYFILE}"
    fi

    if ! caddy validate --config "${CADDYFILE}" >/dev/null 2>&1; then
        if [[ -n "${backup}" ]]; then
            mv "${backup}" "${CADDYFILE}"
            warn "Caddy 配置校验失败，已回滚本次追加，原配置完好。"
        else
            rm -f "${CADDYFILE}"
            warn "Caddy 配置校验失败 (新建配置场景)，已移除。"
        fi
        die "请人工检查后重试: caddy validate --config ${CADDYFILE}"
    fi

    systemctl enable caddy >/dev/null 2>&1 || true
    if systemctl reload caddy >/dev/null 2>&1 || systemctl restart caddy >/dev/null 2>&1; then
        ok "Caddy 站点配置已追加并重载生效。"
    else
        warn "Caddy 重载未成功，请手动执行: systemctl restart caddy"
        return 1
    fi
}

# ---------------- systemd ----------------

write_service() {
    local supplementary=""
    getent group caddy >/dev/null 2>&1 && supplementary="SupplementaryGroups=caddy"

    cat > "${SYSTEMD_FILE}" <<EOF
[Unit]
Description=Emby Proxy Statistics Service (Go Edition)
After=network.target caddy.service

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_USER}
${supplementary}
WorkingDirectory=${INSTALL_DIR}
ExecStart=${BIN_FILE}
Restart=always
RestartSec=3
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
ProtectClock=true
RestrictSUIDSGID=true
RestrictNamespaces=true
RestrictRealtime=true
LockPersonality=true
MemoryDenyWriteExecute=true
ReadWritePaths=${INSTALL_DIR}/data

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
}

health_wait() {
    local probe="curl"
    command -v curl >/dev/null 2>&1 || probe="wget"

    for _ in $(seq 1 15); do
        if [[ "${probe}" = "curl" ]]; then
            curl -sf -m 2 "http://127.0.0.1:${API_PORT}/api/health" >/dev/null 2>&1 && return 0
        else
            wget -q -T 2 -O /dev/null "http://127.0.0.1:${API_PORT}/api/health" 2>/dev/null && return 0
        fi
        sleep 1
    done
    return 1
}

print_summary() {
    if ! systemctl is-active "${SERVICE_NAME}" >/dev/null 2>&1; then
        warn "服务未运行，请检查: journalctl -u ${SERVICE_NAME} -e"
        return 1
    fi
    ok "服务运行中: systemctl status ${SERVICE_NAME}"
    echo ""
    echo "=================================================="
    echo "🎉 完成！仪表盘: ${BASE_URL}"
    echo "   管理账号: ${INPUT_USER} (密码为安装时设置的密码)"
    echo "   配置文件: ${CONFIG_FILE}"
    echo "   数据目录: ${INSTALL_DIR}/data"
    echo ""
    echo "别忘了把域名 ${INPUT_DOMAIN} 的 DNS A 记录解析到本机公网 IP。"
    if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
        echo "   提示: ufw 已启用，请确认已放行 80/443 (sudo ufw allow 80,443/tcp)"
    elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
        echo "   提示: firewalld 运行中，请确认已放行 http/https 服务"
    fi
    echo "=================================================="
}

# ---------------- 动作 ----------------

do_install() {
    echo "=================================================="
    echo "⚡ Emby Proxy Toolbox 全新安装"
    echo "=================================================="

    command -v systemctl >/dev/null 2>&1 || die "未检测到 systemd，本脚本仅支持 systemd 发行版"
    if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
        die "缺少 curl 或 wget，请先安装其一 (apt install -y curl)"
    fi

    install_caddy_prompt || true

    wizard

    info "创建服务用户与目录..."
    mkdir -p "${INSTALL_DIR}/data"
    if ! id -u "${SERVICE_USER}" >/dev/null 2>&1; then
        useradd --system --home-dir "${INSTALL_DIR}" --shell /usr/sbin/nologin "${SERVICE_USER}"
    fi
    chown "${SERVICE_USER}:${SERVICE_USER}" "${INSTALL_DIR}/data"
    chmod 750 "${INSTALL_DIR}/data"
    if getent group caddy >/dev/null 2>&1; then
        mkdir -p /var/log/caddy
        chown -R caddy:caddy /var/log/caddy 2>/dev/null || true
    fi

    if [[ -f "${BIN_FILE}" ]]; then
        info "检测到已有二进制，跳过获取 (换版本请用 update 子命令)。"
        smoke_test_binary "${BIN_FILE}" || die "已有二进制自检失败: ${BIN_FILE}"
    else
        info "获取程序二进制..."
        acquire_binary "${BIN_FILE}.new" || die "无法获取二进制 (下载失败且无本地源码)，请检查网络后重试"
        smoke_test_binary "${BIN_FILE}.new" || die "二进制自检失败: ${BIN_FILE}.new"
        mv "${BIN_FILE}.new" "${BIN_FILE}"
    fi

    write_config
    write_service

    info "启动服务..."
    systemctl enable --now "${SERVICE_NAME}" >/dev/null 2>&1 || true
    systemctl restart "${SERVICE_NAME}"

    info "应用 Caddy 站点配置 (追加，不覆盖已有配置)..."
    apply_caddy_site || true

    if health_wait; then
        ok "健康检查通过: http://127.0.0.1:${API_PORT}/api/health"
    else
        warn "健康检查未通过，请查看日志: journalctl -u ${SERVICE_NAME} -e"
    fi
    print_summary || true
}

do_update() {
    installed || die "尚未安装 (未找到 ${BIN_FILE} 或 systemd 服务)，请先运行全新安装。"

    local backup="${BIN_FILE}.bak.$(date +%Y%m%d%H%M%S)"
    cp -a "${BIN_FILE}" "${backup}"
    info "已备份当前版本: ${backup}"

    info "获取最新版本..."
    acquire_binary "${BIN_FILE}.new" || {
        warn "获取新版本失败，保留原版本继续运行。"
        rm -f "${BIN_FILE}.new"
        exit 1
    }
    if ! smoke_test_binary "${BIN_FILE}.new"; then
        warn "新版本自检失败，保留原版本继续运行。"
        rm -f "${BIN_FILE}.new"
        exit 1
    fi

    mv "${BIN_FILE}.new" "${BIN_FILE}"
    chmod 755 "${BIN_FILE}"
    chown root:root "${BIN_FILE}"

    systemctl daemon-reload
    systemctl restart "${SERVICE_NAME}"

    if health_wait; then
        ok "更新完成，服务健康。配置与统计数据未受影响。"
    else
        warn "更新后健康检查未通过！回滚命令:"
        echo "  systemctl stop ${SERVICE_NAME} && cp -a ${backup} ${BIN_FILE} && systemctl start ${SERVICE_NAME}"
        exit 1
    fi
    echo "旧版本备份保留于: ${backup}"
}

do_reconfigure() {
    [[ -f "${BIN_FILE}" ]] || die "尚未安装程序，请先运行全新安装。"
    if [[ -f "${CONFIG_FILE}" ]]; then
        local cfg_backup="${CONFIG_FILE}.bak.$(date +%Y%m%d%H%M%S)"
        cp -a "${CONFIG_FILE}" "${cfg_backup}"
        info "已备份现有配置: ${cfg_backup}"
    fi

    wizard
    write_config
    systemctl restart "${SERVICE_NAME}"

    if health_wait; then
        ok "重新配置完成，服务健康。"
    else
        warn "健康检查未通过，请查看: journalctl -u ${SERVICE_NAME} -e"
    fi
    print_summary || true
}

do_status() {
    systemctl status "${SERVICE_NAME}" --no-pager || true
}

show_menu() {
    echo "=================================================="
    echo "⚡ Emby Proxy Toolbox 管理菜单"
    echo "=================================================="
    echo "检测到已有安装，请选择操作:"
    echo "  1) 更新程序到最新 Release"
    echo "  2) 重新配置 (向导)"
    echo "  3) 重新应用 Caddy 站点配置 (追加/修复)"
    echo "  4) 查看服务状态"
    echo "  0) 退出"
    local choice=""
    ask "请输入编号" choice
    case "${choice}" in
        1) do_update ;;
        2) do_reconfigure ;;
        3) apply_caddy_site || true ;;
        4) do_status ;;
        *) echo "已退出。" ;;
    esac
}

main() {
    require_root
    local action="${1:-}"
    case "${action}" in
        update) do_update ;;
        reconfigure) do_reconfigure ;;
        caddy) apply_caddy_site || true ;;
        status) do_status ;;
        "")
            if installed; then
                show_menu
            else
                do_install
            fi
            ;;
        *)
            echo "用法: sudo bash $0 [update|reconfigure|caddy|status]"
            echo "不带参数运行时: 已安装则弹管理菜单，否则进入全新安装。"
            exit 1
            ;;
    esac
}

main "$@"
