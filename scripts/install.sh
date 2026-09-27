#!/bin/sh
# Install box on a Linux server or a Linux computer.
# Run it in a terminal. Piping curl into sh cannot answer the prompts.
#
#   curl -fsSL -o install.sh https://raw.githubusercontent.com/4fuu/box/main/scripts/install.sh
#   sh install.sh
#
# Non-interactive:
#   BOX_LANG=en BOX_ROLE=server BOX_DOMAIN=box.example.com BOX_ASSUME_YES=1 sh install.sh
#   BOX_LANG=en BOX_ROLE=computer BOX_DOMAIN=box.example.com BOX_ASSUME_YES=1 sh install.sh
# BOX_START defaults to 0, so services are not started unless BOX_START=1.
# BOX_DRY_RUN=1 prints the plan and does not install anything.
set -eu

repo=${BOX_REPOSITORY:-4fuu/box}
prefix=${BOX_PREFIX:-/usr/local}
lang=${BOX_LANG:-}
role=${BOX_ROLE:-}
version=${BOX_VERSION-}
domain=${BOX_DOMAIN:-}
use_systemd=${BOX_SYSTEMD:-}
start_now=${BOX_START:-}
configured=${BOX_CONFIGURED:-0}
dry=${BOX_DRY_RUN:-0}
assume=${BOX_ASSUME_YES:-0}
tmp_dir=

cleanup() {
    if [ -n "$tmp_dir" ]; then
        rm -rf "$tmp_dir"
    fi
}
trap cleanup EXIT HUP INT TERM

fail() {
    printf '%s\n' "$*" >&2
    exit 1
}

say() {
    if [ "$lang" = zh ]; then
        say_zh "$@"
    else
        say_en "$@"
    fi
}

say_en() {
    case $1 in
        not_linux) printf '%s\n' "box installs on Linux." ;;
        bad_arch) printf '%s\n' "Unsupported architecture: $2" ;;
        need_tty) printf '%s\n' "Run this script in a terminal, or set BOX_LANG and BOX_ROLE." ;;
        role_prompt) printf '%s\n' "What should this machine run?" ;;
        role_server) printf '%s\n' "  1) Server — SSH, HTTP, and QUIC" ;;
        role_computer) printf '%s\n' "  2) Computer — join a server and run the agent" ;;
        version_prompt) printf '%s\n' "Release (empty for latest):" ;;
        bad_version) printf '%s\n' "Version must look like 2026.924.0" ;;
        domain_prompt) printf '%s\n' "Parent domain for this server:" ;;
        bad_domain) printf '%s\n' "Enter a domain such as box.example.com" ;;
        join_host_prompt) printf '%s\n' "Server to join (host or host:port):" ;;
        bad_host) printf '%s\n' "Enter a host such as box.example.com or box.example.com:22" ;;
        systemd_prompt) printf '%s\n' "Install systemd units? [Y/n]" ;;
        start_prompt) printf '%s\n' "Start services now? [y/N]" ;;
        proceed_prompt) printf '%s\n' "Install with this plan? [y/N]" ;;
        cancelled) printf '%s\n' "Cancelled." ;;
        invalid_choice) printf '%s\n' "Choose 1 or 2." ;;
        plan) printf '%s\n' "Plan" ;;
        plan_server) printf '%s\n' "Role: server" ;;
        plan_computer) printf '%s\n' "Role: computer" ;;
        plan_version) printf '%s\n' "Release: $2" ;;
        plan_domain) printf '%s\n' "Domain: $2" ;;
        plan_join) printf '%s\n' "Join: $2" ;;
        plan_ports) printf '%s\n' "Ports: SSH :22, HTTP :80, QUIC :7443" ;;
        plan_systemd) printf '%s\n' "systemd: $2" ;;
        plan_start) printf '%s\n' "Start now: $2" ;;
        yes) printf '%s' "yes" ;;
        no) printf '%s' "no" ;;
        packages) printf '%s\n' "Installing packages." ;;
        no_packages) printf '%s\n' "No supported package manager (apt-get, dnf, or pacman)." ;;
        installing_box) printf '%s\n' "Installing box $2." ;;
        checksum) printf '%s\n' "Checksum verification failed for $2" ;;
        missing_sum) printf '%s\n' "SHA256SUMS has no entry for $2" ;;
        units) printf '%s\n' "Installed systemd units." ;;
        started) printf '%s\n' "Started services." ;;
        server_ports) printf '%s\n' "Open three ports in the firewall: SSH (default :22), HTTP (default :80), and QUIC (default :7443)." ;;
        server_run) printf '%s\n' "Start the server: box serve --domain $2" ;;
        server_unit) printf '%s\n' "Or enable the unit: systemctl enable --now box.service" ;;
        password_hint) printf '%s\n' "The first start prints a one-time password. With the systemd unit it is in: journalctl -u box.service -n 30" ;;
        joining) printf '%s\n' "Joining $2. Approve the code at the server." ;;
        agent_run) printf '%s\n' "Run the agent: box agent" ;;
        agent_unit) printf '%s\n' "Enable the user unit: systemctl --user daemon-reload && systemctl --user enable --now box-agent.service" ;;
        agent_linger) printf '%s\n' "To start at boot without a login: loginctl enable-linger" ;;
        installed) printf '%s\n' "box $2 is installed at $3/bin/box" ;;
        dry) printf '%s\n' "Dry run. Nothing was installed." ;;
        *) printf '%s\n' "$1" ;;
    esac
}

say_zh() {
    case $1 in
        not_linux) printf '%s\n' "box 只在 Linux 上安装。" ;;
        bad_arch) printf '%s\n' "不支持的架构：$2" ;;
        need_tty) printf '%s\n' "请在终端里运行此脚本，或设置 BOX_LANG 和 BOX_ROLE。" ;;
        role_prompt) printf '%s\n' "这台机器要担任什么角色？" ;;
        role_server) printf '%s\n' "  1) 服务器 — SSH、HTTP 和 QUIC" ;;
        role_computer) printf '%s\n' "  2) 计算机 — 加入服务器并运行 agent" ;;
        version_prompt) printf '%s\n' "版本（留空表示最新）：" ;;
        bad_version) printf '%s\n' "版本格式应为 2026.924.0" ;;
        domain_prompt) printf '%s\n' "服务器的父域名：" ;;
        bad_domain) printf '%s\n' "请输入类似 box.example.com 的域名" ;;
        join_host_prompt) printf '%s\n' "要加入的服务器（域名或 域名:端口）：" ;;
        bad_host) printf '%s\n' "请输入域名，或域名:端口，例如 box.example.com:22" ;;
        systemd_prompt) printf '%s\n' "安装 systemd 单元？[Y/n]" ;;
        start_prompt) printf '%s\n' "现在启动服务？[y/N]" ;;
        proceed_prompt) printf '%s\n' "按此计划安装？[y/N]" ;;
        cancelled) printf '%s\n' "已取消。" ;;
        invalid_choice) printf '%s\n' "请选择 1 或 2。" ;;
        plan) printf '%s\n' "计划" ;;
        plan_server) printf '%s\n' "角色：服务器" ;;
        plan_computer) printf '%s\n' "角色：计算机" ;;
        plan_version) printf '%s\n' "版本：$2" ;;
        plan_domain) printf '%s\n' "域名：$2" ;;
        plan_join) printf '%s\n' "加入：$2" ;;
        plan_ports) printf '%s\n' "端口：SSH :22、HTTP :80、QUIC :7443" ;;
        plan_systemd) printf '%s\n' "systemd：$2" ;;
        plan_start) printf '%s\n' "立即启动：$2" ;;
        yes) printf '%s' "是" ;;
        no) printf '%s' "否" ;;
        packages) printf '%s\n' "正在安装软件包。" ;;
        no_packages) printf '%s\n' "没有可用的包管理器（apt-get、dnf 或 pacman）。" ;;
        installing_box) printf '%s\n' "正在安装 box $2。" ;;
        checksum) printf '%s\n' "$2 的校验和验证失败" ;;
        missing_sum) printf '%s\n' "SHA256SUMS 里没有 $2" ;;
        units) printf '%s\n' "已安装 systemd 单元。" ;;
        started) printf '%s\n' "已启动服务。" ;;
        server_ports) printf '%s\n' "请在防火墙上放行三个端口：SSH（默认 :22）、HTTP（默认 :80）和 QUIC（默认 :7443）。" ;;
        server_run) printf '%s\n' "启动服务器：box serve --domain $2" ;;
        server_unit) printf '%s\n' "或启用单元：systemctl enable --now box.service" ;;
        password_hint) printf '%s\n' "第一次启动会打印一次性密码。若使用 systemd，它在：journalctl -u box.service -n 30" ;;
        joining) printf '%s\n' "正在加入 $2。请在服务器上批准验证码。" ;;
        agent_run) printf '%s\n' "运行代理：box agent" ;;
        agent_unit) printf '%s\n' "启用用户单元：systemctl --user daemon-reload && systemctl --user enable --now box-agent.service" ;;
        agent_linger) printf '%s\n' "若要在未登录时开机启动：loginctl enable-linger" ;;
        installed) printf '%s\n' "box $2 已安装到 $3/bin/box" ;;
        dry) printf '%s\n' "这是演练，没有安装任何东西。" ;;
        *) printf '%s\n' "$1" ;;
    esac
}

read_line() {
    if [ "$assume" = 1 ]; then
        printf '%s' "$1"
        return 0
    fi
    IFS= read -r line || line=
    printf '%s' "$line"
}

is_yes() {
    case $1 in
        y|Y|yes|YES|是) return 0 ;;
        *) return 1 ;;
    esac
}

is_no() {
    case $1 in
        n|N|no|NO|否) return 0 ;;
        *) return 1 ;;
    esac
}

choose_lang() {
    if [ -n "$lang" ]; then
        case $lang in
            en|zh) return 0 ;;
            *) fail "BOX_LANG must be en or zh" ;;
        esac
    fi
    printf '%s\n' "Language / 语言:"
    printf '%s\n' "  1) English"
    printf '%s\n' "  2) 中文"
    printf '%s' "> "
    choice=$(read_line "")
    case $choice in
        1|en|English) lang=en ;;
        2|zh|中文) lang=zh ;;
        *)
            printf '%s\n' "Choose 1 or 2. Set BOX_LANG=en or BOX_LANG=zh when stdin is not a terminal." >&2
            printf '%s\n' "请选择 1 或 2。标准输入不是终端时请设置 BOX_LANG=en 或 BOX_LANG=zh。" >&2
            exit 1
            ;;
    esac
}

choose_role() {
    if [ -n "$role" ]; then
        case $role in
            server|computer) return 0 ;;
            *) fail "BOX_ROLE must be server or computer" ;;
        esac
    fi
    say role_prompt
    say role_server
    say role_computer
    printf '%s' "> "
    choice=$(read_line "")
    case $choice in
        1|server) role=server ;;
        2|computer) role=computer ;;
        *) say invalid_choice >&2; exit 1 ;;
    esac
}

choose_version() {
    if [ -n "$version" ]; then
        return 0
    fi
    if [ "$assume" = 1 ]; then
        version=latest
        return 0
    fi
    say version_prompt
    version=$(read_line "")
    if [ -z "$version" ]; then
        version=latest
    fi
}

valid_version() {
    printf '%s\n' "$1" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'
}

valid_domain() {
    printf '%s\n' "$1" | grep -Eq '^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$' \
        && ! printf '%s\n' "$1" | grep -q '\.\.'
}

valid_join() {
    target=$1
    case $target in
        *:*)
            host=${target%:*}
            port=${target##*:}
            case $port in
                *[!0-9]*|'') return 1 ;;
            esac
            case $host in
                *:*) return 1 ;;
            esac
            valid_domain "$host"
            ;;
        *)
            valid_domain "$target"
            ;;
    esac
}

choose_details() {
    if [ -z "$domain" ]; then
        if [ "$assume" = 1 ]; then
            fail "BOX_DOMAIN is required"
        fi
        if [ "$role" = server ]; then
            say domain_prompt
        else
            say join_host_prompt
        fi
        domain=$(read_line "")
    fi
    domain=$(printf '%s' "$domain" | tr '[:upper:]' '[:lower:]')
    domain=${domain%.}
    if [ "$role" = server ]; then
        valid_domain "$domain" || fail "$(say bad_domain)"
    else
        valid_join "$domain" || fail "$(say bad_host)"
    fi
    if [ -z "$use_systemd" ]; then
        if [ "$assume" = 1 ]; then
            use_systemd=0
        else
            say systemd_prompt
            answer=$(read_line "")
            if is_no "$answer"; then
                use_systemd=0
            else
                use_systemd=1
            fi
        fi
    fi
    if [ -z "$start_now" ]; then
        if [ "$use_systemd" != 1 ] || [ "$assume" = 1 ]; then
            start_now=0
        else
            say start_prompt
            answer=$(read_line "")
            if is_yes "$answer"; then
                start_now=1
            else
                start_now=0
            fi
        fi
    fi
}

yn_word() {
    if [ "$1" = 1 ]; then
        say yes
    else
        say no
    fi
}

show_plan() {
    say plan
    if [ "$role" = server ]; then
        say plan_server
        say plan_domain "$domain"
        say plan_ports
    else
        say plan_computer
        say plan_join "$domain"
    fi
    shown=$version
    if [ -z "$shown" ]; then
        shown=latest
    fi
    say plan_version "$shown"
    say plan_systemd "$(yn_word "$use_systemd")"
    say plan_start "$(yn_word "$start_now")"
}

confirm_plan() {
    if [ "$assume" = 1 ] || [ "$dry" = 1 ]; then
        return 0
    fi
    say proceed_prompt
    answer=$(read_line "")
    if is_yes "$answer"; then
        return 0
    fi
    say cancelled
    exit 0
}

arch_of() {
    case $(uname -m) in
        x86_64|amd64) printf '%s\n' amd64 ;;
        aarch64|arm64) printf '%s\n' arm64 ;;
        *) say bad_arch "$(uname -m)" >&2; exit 1 ;;
    esac
}

need_cmd() {
    command -v "$1" >/dev/null 2>&1
}

install_packages() {
    say packages
    if [ "$dry" = 1 ]; then
        return 0
    fi
    if need_cmd dnf; then
        dnf install -y curl tar ca-certificates
    elif need_cmd apt-get; then
        export DEBIAN_FRONTEND=noninteractive
        apt-get update
        apt-get install -y curl tar ca-certificates
    elif need_cmd pacman; then
        pacman -Sy --noconfirm curl tar ca-certificates
    else
        say no_packages >&2
        exit 1
    fi
}

sha256_file() {
    if need_cmd sha256sum; then
        sha256sum "$1" | awk '{ print $1 }'
    elif need_cmd shasum; then
        shasum -a 256 "$1" | awk '{ print $1 }'
    else
        fail "sha256sum or shasum is required"
    fi
}

gh_token() {
    if [ -n "${GH_TOKEN:-}" ]; then
        printf '%s' "$GH_TOKEN"
    elif [ -n "${GITHUB_TOKEN:-}" ]; then
        printf '%s' "$GITHUB_TOKEN"
    fi
}

download() {
    url=$1
    dest=$2
    n=1
    token=$(gh_token)
    while [ "$n" -le 6 ]; do
        if [ -n "$token" ]; then
            curl --proto '=https' --tlsv1.2 -fL \
                -H "Authorization: Bearer $token" \
                -H "Accept: application/octet-stream" \
                "$url" -o "$dest" && return 0
        else
            curl --proto '=https' --tlsv1.2 -fL "$url" -o "$dest" && return 0
        fi
        n=$((n + 1))
        sleep 5
    done
    fail "download failed: $url"
}

resolve_version() {
    if [ "$version" = latest ] || [ -z "$version" ]; then
        token=$(gh_token)
        if [ -n "$token" ]; then
            tag=$(curl --proto '=https' --tlsv1.2 -fsSL \
                -H "Authorization: Bearer $token" \
                -H "Accept: application/vnd.github+json" \
                "https://api.github.com/repos/$repo/releases/latest" \
                | awk -F'"' '/tag_name/ { print $4; exit }')
        else
            release_url=$(curl --proto '=https' --tlsv1.2 -fsSL -o /dev/null \
                -w '%{url_effective}' "https://github.com/$repo/releases/latest")
            tag=${release_url##*/}
        fi
        version=${tag#v}
    fi
    case $version in
        v*) version=${version#v} ;;
    esac
    valid_version "$version" || fail "$(say bad_version)"
    tag=v$version
}

asset_url() {
    want=$1
    awk -v want="$want" '
        $0 ~ /"url": "https:\/\/api.github.com\/repos\/.*\/releases\/assets\// {
            url = $0
            sub(/.*"url": "/, "", url)
            sub(/".*/, "", url)
        }
        $0 ~ /"name":/ {
            name = $0
            sub(/.*"name": "/, "", name)
            sub(/".*/, "", name)
            if (name == want && url != "") print url
        }
    '
}

install_box() {
    arch=$1
    asset="box-$version-linux-$arch.tar.gz"
    say installing_box "$version"
    if [ "$dry" = 1 ]; then
        return 0
    fi
    token=$(gh_token)
    if [ -n "$token" ]; then
        rel=$(curl --proto '=https' --tlsv1.2 -fsSL \
            -H "Authorization: Bearer $token" \
            -H "Accept: application/vnd.github+json" \
            "https://api.github.com/repos/$repo/releases/tags/$tag")
        box_url=$(printf '%s\n' "$rel" | asset_url "$asset")
        sum_url=$(printf '%s\n' "$rel" | asset_url "SHA256SUMS")
        if [ -z "$box_url" ] || [ -z "$sum_url" ]; then
            fail "release $tag is missing $asset"
        fi
        download "$box_url" "$tmp_dir/$asset"
        download "$sum_url" "$tmp_dir/SHA256SUMS"
    else
        base="https://github.com/$repo/releases/download/$tag"
        download "$base/$asset" "$tmp_dir/$asset"
        download "$base/SHA256SUMS" "$tmp_dir/SHA256SUMS"
    fi
    expected=$(awk -v asset="$asset" '$2 == asset || $2 == "*" asset { print $1 }' "$tmp_dir/SHA256SUMS")
    [ -n "$expected" ] || fail "$(say missing_sum "$asset")"
    actual=$(sha256_file "$tmp_dir/$asset")
    [ "$actual" = "$expected" ] || fail "$(say checksum "$asset")"
    mkdir -p "$tmp_dir/box"
    tar -xzf "$tmp_dir/$asset" -C "$tmp_dir/box"
    [ -f "$tmp_dir/box/box" ] || fail "release archive does not contain box"
    install -m 0755 "$tmp_dir/box/box" "$prefix/bin/box"
}

write_server_unit() {
    unit_dir=/etc/systemd/system
    mkdir -p "$unit_dir"
    cat > "$unit_dir/box.service" <<EOF
[Unit]
Description=box server
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=$prefix/bin/box serve --domain $domain
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
EOF
}

write_agent_unit() {
    unit_dir=/etc/systemd/user
    mkdir -p "$unit_dir"
    cat > "$unit_dir/box-agent.service" <<EOF
[Unit]
Description=box agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=$prefix/bin/box agent
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
EOF
}

enable_user_agent() {
    if [ "$start_now" != 1 ]; then
        say agent_unit
        say agent_linger
        return 0
    fi
    user=${SUDO_USER:-}
    if [ -z "$user" ] || [ "$user" = root ]; then
        if [ "$(id -u)" -ne 0 ]; then
            systemctl --user daemon-reload
            systemctl --user enable --now box-agent.service
            say started
            say agent_linger
            return 0
        fi
        say agent_unit
        say agent_linger
        return 0
    fi
    uid=$(id -u "$user")
    rundir=/run/user/$uid
    if [ -d "$rundir" ]; then
        if sudo -u "$user" \
            XDG_RUNTIME_DIR="$rundir" \
            DBUS_SESSION_BUS_ADDRESS="unix:path=$rundir/bus" \
            systemctl --user daemon-reload \
            && sudo -u "$user" \
            XDG_RUNTIME_DIR="$rundir" \
            DBUS_SESSION_BUS_ADDRESS="unix:path=$rundir/bus" \
            systemctl --user enable --now box-agent.service
        then
            say started
            say agent_linger
            return 0
        fi
    fi
    say agent_unit
    say agent_linger
}

write_units() {
    if [ "$use_systemd" != 1 ]; then
        return 0
    fi
    if [ "$dry" = 1 ]; then
        say units
        return 0
    fi
    if ! need_cmd systemctl; then
        return 0
    fi
    if [ "$role" = server ]; then
        write_server_unit
        systemctl daemon-reload
        say units
        if [ "$start_now" = 1 ]; then
            systemctl enable --now box.service
            say started
            say password_hint
        else
            say server_unit
        fi
    else
        write_agent_unit
        say units
        enable_user_agent
    fi
}

join_computer() {
    if [ "$role" != computer ]; then
        return 0
    fi
    if [ "$dry" = 1 ]; then
        return 0
    fi
    say joining "$domain"
    if [ "$(id -u)" -eq 0 ] && [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ]; then
        sudo -u "$SUDO_USER" -- "$prefix/bin/box" join "$domain"
        return 0
    fi
    "$prefix/bin/box" join "$domain"
}

become_root() {
    if [ "$dry" = 1 ] || [ "$(id -u)" -eq 0 ]; then
        return 0
    fi
    script=$0
    case $script in
        /*) ;;
        *) script=$(pwd)/$script ;;
    esac
    exec sudo \
        GH_TOKEN="${GH_TOKEN:-}" \
        GITHUB_TOKEN="${GITHUB_TOKEN:-}" \
        BOX_CONFIGURED=1 \
        BOX_LANG="$lang" \
        BOX_ROLE="$role" \
        BOX_VERSION="$version" \
        BOX_DOMAIN="$domain" \
        BOX_SYSTEMD="$use_systemd" \
        BOX_START="$start_now" \
        BOX_REPOSITORY="$repo" \
        BOX_PREFIX="$prefix" \
        "$script"
}

configure() {
    if [ "$(uname -s)" != Linux ]; then
        printf '%s\n' "box installs on Linux. / box 只在 Linux 上安装。" >&2
        exit 1
    fi
    choose_lang
    choose_role
    choose_version
    if [ "$version" != latest ]; then
        valid_version "$version" || fail "$(say bad_version)"
    fi
    choose_details
    show_plan
    confirm_plan
}

finish_messages() {
    if [ "$role" = server ]; then
        say server_ports
        say server_run "$domain"
        if [ "$start_now" != 1 ]; then
            say password_hint
        fi
        return 0
    fi
    if [ "$use_systemd" != 1 ]; then
        say agent_run
    fi
}

main() {
    if [ "$configured" != 1 ]; then
        configure
        become_root
    else
        [ -n "$lang" ] || lang=en
        [ -n "$role" ] || fail "BOX_ROLE is required"
    fi
    if [ "$dry" = 1 ]; then
        say dry
        exit 0
    fi
    arch=$(arch_of)
    install_packages
    tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/box-install.XXXXXX")
    resolve_version
    install_box "$arch"
    join_computer
    write_units
    say installed "$version" "$prefix"
    finish_messages
}

main
