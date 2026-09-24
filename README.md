# box

Persistent Linux computers on your own machines, reached with the `ssh` you already have.

`ssh box.example.com` opens a control REPL. `new` creates a computer on a deploy node you paired. `ssh web@box.example.com` opens a shell in the container named `web`. The username selects that container. One node runs many containers, and each has its own sshd. The session is inside the container, not on the node. From inside the container, `box domain` prints the domain configured at server start, and `box portal add web 3000` claims `web` under that domain for port 3000. An agent reads the domain from `box`. It does not invent one. The server routes `Host` to that port. An edge you run publishes the server's fixed HTTP port. The node may sit behind NAT. [frp](https://github.com/fatedier/frp) carries commands, SSH, and HTTP between the server and the node. Each computer is a rootful [Podman](https://podman.io) container with its own volume.

There is no account. The server prints a one-time password at init. The first SSH client to present it is bound. Further clients get a password from a bound client, or from a localhost command on the server. A node joins with a short pairing code.

## Status

The `box` binary implements the server, the node controller, and the guest CLI. `go test ./...` exercises that control plane without Podman, frp, or a built image.

A deploy node is not usable until it has pulled an image. The base image is Fedora 44 with systemd, sshd, agent tools, mise, Go, Rust, Node, Python, and a `box` user. Its Dockerfile is [images/base/Dockerfile](images/base/Dockerfile).

## Fit

Use this when you want exe.dev's "it is just a computer" on hardware you operate: a persistent disk, sudo, a hostname routed to one port, and stock `ssh` for both control and login.

Do not use this as a multi-tenant host. Computers on a node share that node's kernel. The HTTP port is plain and fixed. Put your own edge in front of it. Non-HTTP ports are not published. Computers are not moved between nodes.

## Install

On the Linux machine that will be the server or a deploy node, download the installer and run it in a terminal. It asks for English or Chinese, then whether this machine is the server or a deploy node, and installs `box` plus the programs that role needs (`frps` on a server, Podman and `frpc` on a node).

```bash
curl -fsSL -o install.sh https://raw.githubusercontent.com/4fuu/box/main/scripts/install.sh
sh install.sh
```

Releases are date versions such as `2026.924.0`. The same release publishes the base computer image at `ghcr.io/4fuu/box:<version>`. After a node is paired, register and pull it with `image add` and `image pull` before `new`. See [docs/release.md](docs/release.md).

## First session

Once a server exists, the intended first session is:

```
ssh box.example.com
```

Present the one-time password from server init. Then:

```
box ▶ node pair
box ▶ image pull base
box ▶ new web
box ▶ ssh web
```

`new` prints `ssh web@box.example.com`. That destination is what `scp` and VS Code Remote-SSH use.

On the computer, `box portal check` asks whether a hostname is free. `box portal add` claims it for a port inside that container. The server accepts the claim before frp routes it. `key copy` prints the server's GitHub public key and nothing else. `env set GH_TOKEN <token>` stores a token the server injects when a container starts, so `gh` does not ask for a login. A skill in the base image tells an agent how to claim a portal and how to use mise, including a temporary toolchain.

## Design

[DESIGN.md](DESIGN.md) is the spec: binding, the three parts, frp, the REPL, portals, the single `box` binary, the base image, and what the first version leaves out.
