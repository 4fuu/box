<p align="center">
  <img src="docs/assets/logo.svg" width="128" alt="box">
</p>

# box

[English](README.md) | [简体中文](README.zh-CN.md)

box is a self-hosted persistent Linux computer. The client is the `ssh`
already installed on your machine. `ssh box.example.com` opens a control REPL.
`new` creates a computer on a deploy node you paired. `ssh web@box.example.com`
opens a shell in the container named `web`.

The reference is [exe.dev](https://exe.dev). There, one command yields a
computer, the disk survives restarts, and a site on that computer gets a
hostname. You SSH in, you are root, and you have a normal userspace with
`systemd`. box keeps that shape and runs it on hardware you operate.

One node runs many computers. Each computer is a rootful
[Podman](https://podman.io) container with its own volume and its own sshd.
The session is inside the container, not on the node. The node may sit behind
NAT. [frp](https://github.com/fatedier/frp) carries commands, SSH, and HTTP
between the server and the node.

> [!WARNING]
> This is not a multi-tenant host. Computers on a node share that node's
> kernel. The HTTP port is plain and fixed. Put your own edge in front of it.
> Non-HTTP ports are not published. Computers are not moved between nodes.

## Why box

- **It is just a computer.** The disk survives restarts. You have sudo, a
  login user, and sshd. `scp`, rsync, and VS Code Remote-SSH use the same
  destination as `ssh`.
- **The client is stock OpenSSH.** There is no account and no client to
  install. The first SSH connection that presents the one-time password from
  server init is bound. Later clients get a password from a bound client, or
  from `box pair` on the server machine.
- **One binary, three roles.** `box serve` is the public SSH entry. `box node`
  is the controller on a deploy node. Inside a computer, `box` only answers
  `domain` and `portal`.
- **A hostname for one port.** From inside the computer, `box portal add web
  3000` claims `web` under the domain configured at server start. The server
  routes that `Host` to port 3000. An agent reads the domain from `box
  domain`. It does not invent one.
- **Your machines.** A deploy node is a controller you pair with a short,
  single-use code. It dials out, so it can sit behind NAT.

## Quick start

### Requirements

- a Linux machine for the server, and a Linux machine for each deploy node;
- cgroup v2 and Podman on every deploy node;
- OpenSSH on the machine you connect from.

The server and the node can be the same machine.

### Install

Download the installer and run it in a terminal. It asks for English or
Chinese, then whether this machine is the server or a deploy node.

```bash
curl -fsSL -o install.sh https://raw.githubusercontent.com/4fuu/box/main/scripts/install.sh
sh install.sh
```

A server install adds `box` and `frps`. A node install adds `box`, `frpc`, and
Podman, and checks for cgroup v2. The script can install systemd units. It
does not start them unless you say so. The one-time password from the first
server start is in `journalctl -u box.service`.

Releases use a calendar version such as `2026.924.0`. See
[docs/release.md](docs/release.md).

### Start the first session

```bash
ssh box.example.com
```

Present the one-time password from server init. Then:

```text
box ▶ node pair
box ▶ image pull base
box ▶ new web
box ▶ ssh web
```

`node pair` prints a one-time code. On the deploy node, as root:

```bash
box node join --server box.example.com:7000 --code <code> --name home
box node
```

`image pull base` has to finish before `new`. The release publishes the base
computer image as `ghcr.io/4fuu/box:<version>`. Register that reference, then
pull it:

```text
box ▶ image add base ghcr.io/4fuu/box:2026.924.0
box ▶ image pull base
```

`new` prints `ssh web@box.example.com`. That destination is what `scp` and
VS Code Remote-SSH use. The login user inside the container is `box`. The SSH
username selects the container, not that user.

### Claim a hostname

On the computer:

```bash
box domain
box portal check web
box portal add web 3000
```

`box domain` prints the parent domain, for example `box.example.com`. `web`
becomes `web.box.example.com` and routes to port 3000 in that container. A
label cannot contain a dot. `check` does not claim. `add` refuses when the
label is taken. The process must listen on `0.0.0.0`.

`key copy` prints the server's GitHub public key and nothing else. `env set
GH_TOKEN <token>` stores a token the server injects when a container starts,
so `gh` does not ask for a login. `env ls` prints names, never values.

## Documentation

| Goal | Guide |
| --- | --- |
| Read the spec: binding, frp, the REPL, portals, and what the first version leaves out | [DESIGN.md](docs/DESIGN.md) |
| Cut a dated release | [Release](docs/release.md) |
| See the base computer image | [images/base/Dockerfile](images/base/Dockerfile) |
| See what an agent inside a computer is told | [images/base/skills/box/SKILL.md](images/base/skills/box/SKILL.md) |

[DESIGN.md](docs/DESIGN.md) wins when it disagrees with this page.

## Development

The module is Go. Read [AGENTS.md](AGENTS.md) before changing the repository.

```bash
go test ./...
go build -o box .
```

`go test` does not build the base image and does not need Podman or frp.

## License

AGPL-3.0-only. The full text is in [LICENSE](LICENSE). Copyright 2026 4fuu
and box contributors. If you run a modified box and let users reach it over a
network, you owe those users the source of your version.
