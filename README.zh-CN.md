<p align="center">
  <img src="docs/assets/logo.svg" width="128" alt="box">
</p>

# box

[English](README.md) | [简体中文](README.zh-CN.md)

box 是一台自托管的持久 Linux 计算机。客户端就是你机器上已有的 `ssh`。
`ssh box.example.com` 打开控制 REPL。`new` 在你配对过的部署节点上创建一台计算机。
`ssh web@box.example.com` 进入名为 `web` 的容器。

参照是 [exe.dev](https://exe.dev)。在那里，一条命令得到一台计算机，磁盘在重启后还在，这台计算机上的网站会有一个主机名。你用 SSH 进去，你是 root，用户态是带 `systemd` 的普通系统。box 保留这个形状，并把它跑在你自己的机器上。

一个节点跑多台计算机。每台计算机是一个 rootful 的 [Podman](https://podman.io) 容器，有自己的卷，也有自己的 sshd。会话在容器里，不在节点上。节点可以在 NAT 后面。[frp](https://github.com/fatedier/frp) 在服务器和节点之间运送命令、SSH 和 HTTP。

> [!WARNING]
> 这不是多租户主机。同一节点上的计算机共用该节点的内核。HTTP 端口是明文的，而且是固定的。请在它前面放你自己的边缘。非 HTTP 端口不会发布。计算机不会在节点之间迁移。

## 为什么用 box

- **它就是一台计算机。** 磁盘在重启后还在。你有 sudo、一个登录用户，以及 sshd。`scp`、rsync 和 VS Code Remote-SSH 用的目的地与 `ssh` 相同。
- **客户端是系统里的 OpenSSH。** 没有账号，也不需要另装客户端。服务器初始化时打印的一次性密码，由第一个出示它的 SSH 连接绑定。之后的客户端向已绑定的客户端要密码，或在服务器本机运行 `box pair`。
- **一个二进制，三种角色。** `box serve` 是公共 SSH 入口。`box node` 是部署节点上的控制器。在计算机里，`box` 只有 `domain` 和 `portal`。
- **一个端口对应一个主机名。** 在计算机里，`box portal add web 3000` 在服务器启动时配置的域名下声明 `web`。服务器按这个 `Host` 把请求转到 3000 端口。代理从 `box domain` 读取域名，不自己编一个。
- **机器是你的。** 部署节点用一个短的、一次性的配对码加入。它主动向外连接，所以可以放在 NAT 后面。

## 快速开始

### 要求

- 一台跑服务器的 Linux，以及每台部署节点一台 Linux；
- 每台部署节点有 cgroup v2 和 Podman；
- 你用来连接的机器上有 OpenSSH。

服务器和节点可以是同一台机器。

### 安装

下载安装脚本，在终端里运行。它会询问 English 或中文，然后询问这台机器是服务器还是部署节点。

```bash
curl -fsSL -o install.sh https://raw.githubusercontent.com/4fuu/box/main/scripts/install.sh
sh install.sh
```

服务器安装会加上 `box` 和 `frps`。节点安装会加上 `box`、`frpc` 和 Podman，并检查 cgroup v2。脚本可以安装 systemd 单元。除非你同意，它不会启动服务。服务器第一次启动打印的一次性密码在 `journalctl -u box.service` 里。

版本号是日历版本，例如 `2026.924.0`。见 [docs/release.md](docs/release.md)。

### 第一次会话

```bash
ssh box.example.com
```

出示服务器初始化时的一次性密码。然后：

```text
box ▶ node pair
box ▶ image pull base
box ▶ new web
box ▶ ssh web
```

`node pair` 会打印一次性配对码。在部署节点上，以 root 运行：

```bash
box node join --server box.example.com:7000 --code <code> --name home
box node
```

`new` 之前必须先完成 `image pull base`。发布会把基础计算机镜像推到 `ghcr.io/4fuu/box:<版本>`。先登记这个引用，再拉取：

```text
box ▶ image add base ghcr.io/4fuu/box:2026.924.0
box ▶ image pull base
```

`new` 会打印 `ssh web@box.example.com`。`scp` 和 VS Code Remote-SSH 用的就是这个地址。容器里的登录用户是 `box`。SSH 用户名选择的是容器，不是这个用户。

### 声明主机名

在计算机上：

```bash
box domain
box portal check web
box portal add web 3000
```

`box domain` 打印父域名，例如 `box.example.com`。`web` 变成 `web.box.example.com`，并转到该容器的 3000 端口。标签不能包含点。`check` 不声明。标签已被占用时，`add` 会拒绝。进程必须监听 `0.0.0.0`。

`key copy` 只打印服务器的 GitHub 公钥。`env set GH_TOKEN <token>` 把令牌存在服务器上，容器启动时注入，这样 `gh` 不会要求登录。`env ls` 只打印名字，不打印值。

## 文档

| 目的 | 文档 |
| --- | --- |
| 读规格：绑定、frp、REPL、门户，以及第一版不做的事 | [DESIGN.md](docs/DESIGN.md) |
| 按日期发版 | [Release](docs/release.md) |
| 看基础计算机镜像 | [images/base/Dockerfile](images/base/Dockerfile) |
| 看计算机里的代理会读到什么 | [images/base/skills/box/SKILL.md](images/base/skills/box/SKILL.md) |

与本页不一致时，以 [DESIGN.md](docs/DESIGN.md) 为准。

## 开发

模块是 Go。改仓库之前先读 [AGENTS.md](AGENTS.md)。

```bash
go test ./...
go build -o box .
```

`go test` 不构建基础镜像，也不需要 Podman 或 frp。
