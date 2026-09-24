#!/bin/sh
# Install box on a Linux server or a Linux deploy node.
# Run it in a terminal. Piping curl into sh cannot answer the prompts.
#
#   curl -fsSL -o install.sh https://raw.githubusercontent.com/4fuu/box/main/scripts/install.sh
#   sh install.sh
#
# Non-interactive:
#   BOX_LANG=en BOX_ROLE=server BOX_DOMAIN=box.example.com BOX_ASSUME_YES=1 sh install.sh
# BOX_START defaults to 0, so services are not started unless BOX_START=1.
# BOX_DRY_RUN=1 prints the plan and does not install anything.
set -eu

repo=${BOX_REPOSITORY:-4fuu/box}
frp_version=0.71.0
prefix=${BOX_PREFIX:-/usr/local}
lang=${BOX_LANG:-}
role=${BOX_ROLE:-}
version=${BOX_VERSION-}
domain=${BOX_DOMAIN:-}
join=${BOX_JOIN:-}
server_addr=${BOX_SERVER:-}
code=${BOX_CODE:-}
node_name=${BOX_NAME:-}
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
        role_server) printf '%s\n' "  1) Server — SSH entry, HTTP routing, and frps" ;;
        role_node) printf '%s\n' "  2) Deploy node — Podman and frpc" ;;
        version_prompt) printf '%s\n' "Release (empty for latest):" ;;
        bad_version) printf '%s\n' "Version must look like 2026.924.0" ;;
        domain_prompt) printf '%s\n' "Parent domain for this server:" ;;
        bad_domain) printf '%s\n' "Enter a domain such as box.example.com" ;;
        join_prompt) printf '%s\n' "Join a server now? [y/N]" ;;
        server_prompt) printf '%s\n' "frps address (host:7000):" ;;
        code_prompt) printf '%s\n' "Pairing code:" ;;
        name_prompt) printf '%s\n' "Node name:" ;;
        bad_name) printf '%s\n' "Enter a lowercase node name." ;;
        systemd_prompt) printf '%s\n' "Install systemd units? [Y/n]" ;;
        start_prompt) printf '%s\n' "Start services now? [y/N]" ;;
        proceed_prompt) printf '%s\n' "Install with this plan? [y/N]" ;;
        cancelled) printf '%s\n' "Cancelled." ;;
        invalid_choice) printf '%s\n' "Choose 1 or 2." ;;
        plan) printf '%s\n' "Plan" ;;
        plan_server) printf '%s\n' "Role: server" ;;
        plan_node) printf '%s\n' "Role: deploy node" ;;
        plan_version) printf '%s\n' "Release: $2" ;;
        plan_domain) printf '%s\n' "Domain: $2" ;;
        plan_join) printf '%s\n' "Join: $2 as $3" ;;
        plan_no_join) printf '%s\n' "Join: later" ;;
        plan_systemd) printf '%s\n' "systemd: $2" ;;
        plan_start) printf '%s\n' "Start now: $2" ;;
        yes) printf '%s' "yes" ;;
        no) printf '%s' "no" ;;
        packages) printf '%s\n' "Installing packages." ;;
        no_packages) printf '%s\n' "No supported package manager (apt-get, dnf, or pacman)." ;;
        installing_box) printf '%s\n' "Installing box $2." ;;
        installing_frp) printf '%s\n' "Installing frp $2 ($3)." ;;
        checksum) printf '%s\n' "Checksum verification failed for $2" ;;
        missing_sum) printf '%s\n' "SHA256SUMS has no entry for $2" ;;
        cgroup) printf '%s\n' "A deploy node needs cgroup v2. /sys/fs/cgroup is not cgroup2fs." ;;
        units) printf '%s\n' "Installed systemd units." ;;
        started) printf '%s\n' "Started services." ;;
        password_hint) printf '%s\n' "The one-time password is in the server journal: journalctl -u box.service -n 30" ;;
        server_next) printf '%s\n' "Start later with: systemctl enable --now box.service box-frps.service" ;;
        node_next) printf '%s\n' "Join later, as root, with: box node join --server <host:7000> --code <code> --name <name>" ;;
        node_pull) printf '%s\n' "After the node is online, pull the base image before creating a computer: image pull base" ;;
        image_ref) printf '%s\n' "Published image: ghcr.io/$2:<version>" ;;
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
        role_server) printf '%s\n' "  1) 服务器 — SSH 入口、HTTP 转发，以及 frps" ;;
        role_node) printf '%s\n' "  2) 部署节点 — Podman 和 frpc" ;;
        version_prompt) printf '%s\n' "版本（留空表示最新）：" ;;
        bad_version) printf '%s\n' "版本格式应为 2026.924.0" ;;
        domain_prompt) printf '%s\n' "服务器的父域名：" ;;
        bad_domain) printf '%s\n' "请输入类似 box.example.com 的域名" ;;
        join_prompt) printf '%s\n' "现在就加入一台服务器？[y/N]" ;;
        server_prompt) printf '%s\n' "frps 地址（host:7000）：" ;;
        code_prompt) printf '%s\n' "配对码：" ;;
        name_prompt) printf '%s\n' "节点名称：" ;;
        bad_name) printf '%s\n' "请输入小写的节点名称。" ;;
        systemd_prompt) printf '%s\n' "安装 systemd 单元？[Y/n]" ;;
        start_prompt) printf '%s\n' "现在启动服务？[y/N]" ;;
        proceed_prompt) printf '%s\n' "按此计划安装？[y/N]" ;;
        cancelled) printf '%s\n' "已取消。" ;;
        invalid_choice) printf '%s\n' "请选择 1 或 2。" ;;
        plan) printf '%s\n' "计划" ;;
        plan_server) printf '%s\n' "角色：服务器" ;;
        plan_node) printf '%s\n' "角色：部署节点" ;;
        plan_version) printf '%s\n' "版本：$2" ;;
        plan_domain) printf '%s\n' "域名：$2" ;;
        plan_join) printf '%s\n' "加入：$3 @ $2" ;;
        plan_no_join) printf '%s\n' "加入：稍后再做" ;;
        plan_systemd) printf '%s\n' "systemd：$2" ;;
        plan_start) printf '%s\n' "立即启动：$2" ;;
        yes) printf '%s' "是" ;;
        no) printf '%s' "否" ;;
        packages) printf '%s\n' "正在安装软件包。" ;;
        no_packages) printf '%s\n' "没有可用的包管理器（apt-get、dnf 或 pacman）。" ;;
        installing_box) printf '%s\n' "正在安装 box $2。" ;;
        installing_frp) printf '%s\n' "正在安装 frp $2（$3）。" ;;
        checksum) printf '%s\n' "$2 的校验和验证失败" ;;
        missing_sum) printf '%s\n' "SHA256SUMS 里没有 $2" ;;
        cgroup) printf '%s\n' "部署节点需要 cgroup v2。/sys/fs/cgroup 不是 cgroup2fs。" ;;
        units) printf '%s\n' "已安装 systemd 单元。" ;;
        started) printf '%s\n' "已启动服务。" ;;
        password_hint) printf '%s\n' "一次性密码在服务器日志里：journalctl -u box.service -n 30" ;;
        server_next) printf '%s\n' "稍后启动：systemctl enable --now box.service box-frps.service" ;;
        node_next) printf '%s\n' "稍后以 root 加入：box node join --server <host:7000> --code <配对码> --name <名称>" ;;
        node_pull) printf '%s\n' "节点在线之后，先拉取基础镜像再创建计算机：image pull base" ;;
        image_ref) printf '%s\n' "已发布的镜像：ghcr.io/$2:<版本>" ;;
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
            server|node) return 0 ;;
            *) fail "BOX_ROLE must be server or node" ;;
        esac
    fi
    say role_prompt
    say role_server
    say role_node
    printf '%s' "> "
    choice=$(read_line "")
    case $choice in
        1|server) role=server ;;
        2|node) role=node ;;
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

valid_name() {
    printf '%s\n' "$1" | grep -Eq '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'
}

choose_details() {
    if [ "$role" = server ]; then
        if [ -z "$domain" ]; then
            if [ "$assume" = 1 ]; then
                fail "BOX_DOMAIN is required for a server"
            fi
            say domain_prompt
            domain=$(read_line "")
        fi
        domain=$(printf '%s' "$domain" | tr '[:upper:]' '[:lower:]')
        domain=${domain%.}
        valid_domain "$domain" || fail "$(say bad_domain)"
    else
        if [ -z "$join" ]; then
            if [ "$assume" = 1 ]; then
                join=0
            else
                say join_prompt
                answer=$(read_line "")
                if is_yes "$answer"; then
                    join=1
                else
                    join=0
                fi
            fi
        fi
        if [ "$join" = 1 ]; then
            if [ -z "$server_addr" ]; then
                say server_prompt
                server_addr=$(read_line "")
            fi
            if [ -z "$code" ]; then
                say code_prompt
                code=$(read_line "")
            fi
            if [ -z "$node_name" ]; then
                say name_prompt
                node_name=$(read_line "")
            fi
            if [ -z "$server_addr" ] || [ -z "$code" ]; then
                fail "$(say bad_name)"
            fi
            valid_name "$node_name" || fail "$(say bad_name)"
        fi
    fi
    if [ -z "$use_systemd" ]; then
        if [ "$role" = node ] && [ "$join" != 1 ]; then
            use_systemd=0
        elif [ "$assume" = 1 ]; then
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
    else
        say plan_node
        if [ "$join" = 1 ]; then
            say plan_join "$server_addr" "$node_name"
        else
            say plan_no_join
        fi
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

frp_sha() {
    case $1 in
        amd64) printf '%s\n' "84f27e39f11169f7adcef8e8b70c9329de17747b1f14dad9fb95eef5682ea716" ;;
        arm64) printf '%s\n' "f33c293c275d8fc68c654b6fba8f10b2551d6463d09a9fc9cffb7227eae82266" ;;
        *) return 1 ;;
    esac
}

need_cmd() {
    command -v "$1" >/dev/null 2>&1
}

install_packages() {
    node_pkgs=
    if [ "$role" = node ]; then
        node_pkgs=podman
    fi
    say packages
    if [ "$dry" = 1 ]; then
        return 0
    fi
    if need_cmd dnf; then
        # shellcheck disable=SC2086
        dnf install -y curl tar ca-certificates $node_pkgs
    elif need_cmd apt-get; then
        export DEBIAN_FRONTEND=noninteractive
        apt-get update
        # shellcheck disable=SC2086
        apt-get install -y curl tar ca-certificates $node_pkgs
    elif need_cmd pacman; then
        # shellcheck disable=SC2086
        pacman -Sy --noconfirm curl tar ca-certificates $node_pkgs
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

download() {
    url=$1
    dest=$2
    curl --proto '=https' --tlsv1.2 -fL "$url" -o "$dest"
}

resolve_version() {
    if [ "$version" = latest ] || [ -z "$version" ]; then
        release_url=$(curl --proto '=https' --tlsv1.2 -fsSL -o /dev/null \
            -w '%{url_effective}' "https://github.com/$repo/releases/latest")
        tag=${release_url##*/}
        version=${tag#v}
    fi
    case $version in
        v*) version=${version#v} ;;
    esac
    valid_version "$version" || fail "$(say bad_version)"
    tag=v$version
}

install_box() {
    arch=$1
    asset="box-$version-linux-$arch.tar.gz"
    say installing_box "$version"
    if [ "$dry" = 1 ]; then
        return 0
    fi
    base="https://github.com/$repo/releases/download/$tag"
    download "$base/$asset" "$tmp_dir/$asset"
    download "$base/SHA256SUMS" "$tmp_dir/SHA256SUMS"
    expected=$(awk -v asset="$asset" '$2 == asset || $2 == "*" asset { print $1 }' "$tmp_dir/SHA256SUMS")
    [ -n "$expected" ] || fail "$(say missing_sum "$asset")"
    actual=$(sha256_file "$tmp_dir/$asset")
    [ "$actual" = "$expected" ] || fail "$(say checksum "$asset")"
    mkdir -p "$tmp_dir/box"
    tar -xzf "$tmp_dir/$asset" -C "$tmp_dir/box"
    [ -f "$tmp_dir/box/box" ] || fail "release archive does not contain box"
    install -m 0755 "$tmp_dir/box/box" "$prefix/bin/box"
}

install_frp() {
    arch=$1
    tool=frpc
    if [ "$role" = server ]; then
        tool=frps
    fi
    say installing_frp "$frp_version" "$tool"
    if [ "$dry" = 1 ]; then
        return 0
    fi
    name="frp_${frp_version}_linux_${arch}"
    asset="${name}.tar.gz"
    url="https://github.com/fatedier/frp/releases/download/v${frp_version}/$asset"
    download "$url" "$tmp_dir/$asset"
    actual=$(sha256_file "$tmp_dir/$asset")
    expected=$(frp_sha "$arch")
    [ "$actual" = "$expected" ] || fail "$(say checksum "$asset")"
    tar -xzf "$tmp_dir/$asset" -C "$tmp_dir"
    install -m 0755 "$tmp_dir/$name/$tool" "$prefix/bin/$tool"
}

check_cgroup() {
    if [ "$role" != node ] || [ "$dry" = 1 ]; then
        return 0
    fi
    fstype=$(stat -fc %T /sys/fs/cgroup 2>/dev/null || printf '%s' unknown)
    if [ "$fstype" != cgroup2fs ]; then
        say cgroup >&2
        exit 1
    fi
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
    unit_dir=/etc/systemd/system
    mkdir -p "$unit_dir"
    if [ "$role" = server ]; then
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
        cat > "$unit_dir/box-frps.service" <<EOF
[Unit]
Description=box frps
After=box.service
Requires=box.service

[Service]
ExecStartPre=/bin/sh -c 'test -f /var/lib/box/frps.toml'
ExecStart=$prefix/bin/frps -c /var/lib/box/frps.toml
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
EOF
    else
        cat > "$unit_dir/box-node.service" <<EOF
[Unit]
Description=box deploy node
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=$prefix/bin/box node
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
EOF
    fi
    systemctl daemon-reload
    say units
    if [ "$start_now" = 1 ]; then
        if [ "$role" = server ]; then
            systemctl enable --now box.service box-frps.service
            say started
            say password_hint
        else
            systemctl enable --now box-node.service
            say started
        fi
    elif [ "$role" = server ]; then
        say server_next
    fi
}

join_node() {
    if [ "$role" != node ] || [ "$join" != 1 ]; then
        return 0
    fi
    if [ "$dry" = 1 ]; then
        return 0
    fi
    "$prefix/bin/box" node join --server "$server_addr" --code "$code" --name "$node_name"
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
        BOX_CONFIGURED=1 \
        BOX_LANG="$lang" \
        BOX_ROLE="$role" \
        BOX_VERSION="$version" \
        BOX_DOMAIN="$domain" \
        BOX_JOIN="$join" \
        BOX_SERVER="$server_addr" \
        BOX_CODE="$code" \
        BOX_NAME="$node_name" \
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
    check_cgroup
    tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/box-install.XXXXXX")
    resolve_version
    install_box "$arch"
    install_frp "$arch"
    join_node
    write_units
    say installed "$version" "$prefix"
    if [ "$role" = node ]; then
        say node_pull
        say image_ref "$repo"
        if [ "$join" != 1 ]; then
            say node_next
        fi
    elif [ "$start_now" != 1 ]; then
        say server_next
        say password_hint
    fi
}

main
