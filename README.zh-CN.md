<p align="center">
  <img src="docs/assets/logo.svg" width="128" alt="box">
</p>

# box

[English](README.md) | [简体中文](README.zh-CN.md)

box 把你自己的机器放在同一个 SSH 入口后面。计算机是一整台机器：工作站、家用服务器或虚拟机，不是容器。客户端就是到处都有的 `ssh`。服务器是唯一的公共入口。计算机主动向外连接，所以可以放在 NAT 后面。

`ssh box.example.com` 打开控制 REPL。`ssh web@box.example.com` 在名为 `web` 的计算机上打开 shell，终点是那台机器自己的 sshd。

参照是 [exe.dev](https://exe.dev)：机器有名字，状态会留下，机器上的网站有主机名。这是私人部署，不是托管服务。

> [!WARNING]
> 这不是多租户主机。HTTP 端口只提供门户，而且是明文 HTTP。如果需要 TLS，由你放在这个端口前面的边缘来终结。本项目不签发证书。拦截 UDP 的网络无法运行计算机。

## 为什么用 box

- **它就是那台机器。** `scp`、rsync、SFTP、VS Code Remote-SSH 和 `ssh -L` 能用，是因为它们到达的是普通 sshd。连接断开不会停掉计算机上的进程。
- **客户端是系统里的 OpenSSH。** 没有账号，也不需要另装客户端。服务器初始化时打印的一次性密码，由第一个出示它的 SSH 连接绑定。之后的客户端向已绑定的客户端要密码，或在服务器本机运行 `box pair`。
- **一个二进制。** 服务器运行 `box serve`。计算机先 `box join`，再 `box agent`。在计算机上，`box domain` 和 `box portal` 跟代理说话。没有 Podman，没有 frp，也没有节点。
- **一个端口对应一个主机名。** `box portal add web 3000` 在服务器启动时配置的域名下声明 `web`。服务器按这个 `Host` 把请求转到该计算机的 `127.0.0.1:3000`。未知的 `Host` 得到 421。
- **机器是你的。** 计算机用一个短的、一次性的批准码加入。它主动向外连接，所以可以放在 NAT 后面。

## 快速开始

### 要求

- 一台跑服务器的 Linux，以及每台计算机一台 Linux
- 你用来连接的机器上有 OpenSSH，每台计算机上有 sshd

服务器和计算机可以是同一台机器。代理不需要 root。

### 安装

下载安装脚本，在终端里运行。它会询问 English 或中文，然后询问这台机器是服务器还是计算机。

```bash
curl -fsSL -o install.sh https://raw.githubusercontent.com/4fuu/box/main/scripts/install.sh
sh install.sh
```

脚本安装 `box` 二进制。它不安装容器运行时，也不安装 frp。

服务器安装可以加上 systemd 单元。放行三个端口：SSH（默认 `:22`）、HTTP（默认 `:80`）和 QUIC（默认 `:7443`）。然后：

```bash
box serve --domain box.example.com
```

第一次启动会打印一次性密码。

计算机安装会运行 `box join <域名>`，并可以加上 `box agent` 的 systemd 用户单元。在服务器上批准它打印的验证码。

版本号是日历版本，例如 `2026.924.0`。见 [docs/release.md](docs/release.md)。

### 第一次会话

```bash
ssh box.example.com
```

出示服务器初始化时的一次性密码。在计算机上：

```bash
box join box.example.com
box agent
```

`box join` 会打印批准码。在服务器 TUI 里输入，或从已绑定的客户端运行 `approve <code>`。登录用户是运行 `box join` 的用户，除非 `--user` 指定了这台机器上的另一个账户。

`ssh home@box.example.com` 到达的是那台计算机的 sshd。SSH 用户名选择的是计算机，不是登录用户。

### 声明主机名

在计算机上：

```bash
box domain
box portal check web
box portal add web 3000
```

`box domain` 打印父域名，例如 `box.example.com`。`web` 变成 `web.box.example.com`，并转到该机器 `127.0.0.1` 的 3000 端口。标签不能包含点。`check` 不声明。标签已被占用时，`add` 会拒绝。URL 的端口不是 80 时，URL 里会带上端口。

`env set NAME <value>` 保存变量并推送到在线的计算机。`env ls` 只打印名字，不打印值。

## 文档

| 目的 | 文档 |
| --- | --- |
| 读规格：绑定、隧道、REPL、门户，以及第一版不做的事 | [DESIGN.md](docs/DESIGN.md) |
| 按日期发版 | [Release](docs/release.md) |
| 看计算机上的代理会读到什么 | [internal/agent/skill/SKILL.md](internal/agent/skill/SKILL.md) |

与本页不一致时，以 [DESIGN.md](docs/DESIGN.md) 为准。

## 开发

模块是 Go。改仓库之前先读 [AGENTS.md](AGENTS.md)。

```bash
go test ./...
go build -o box .
```

`go test` 不需要 Podman 或 frp。

## 许可证

AGPL-3.0-only，全文见 [LICENSE](LICENSE)。版权所有 2026 4fuu 及 box 贡献者。
如果你运行修改版 box 并让用户通过网络访问它，就必须向这些用户提供你那
个版本的源代码。
