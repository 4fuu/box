# box

Persistent Linux computers on your own machines, reached with the `ssh` you already have.

`ssh box.example.com` opens a control REPL. `new` creates a computer on a deploy node you paired. `ssh web@box.example.com` opens a shell in the container named `web`. The username selects that container. One node runs many containers, and each has its own sshd. The session is inside the container, not on the node. From inside the container, `box domain` prints the domain configured at server start, and `box portal add web 3000` claims `web` under that domain for port 3000. An agent reads the domain from `box`. It does not invent one. The server routes `Host` to that port. An edge you run publishes the server's fixed HTTP port. The node may sit behind NAT. [frp](https://github.com/fatedier/frp) carries commands, SSH, and HTTP between the server and the node. Each computer is a rootful [Podman](https://podman.io) container with its own volume.

There is no account. The server prints a one-time password at init. The first SSH client to present it is bound. Further clients get a password from a bound client, or from a localhost command on the server. A node joins with a short pairing code.

## Status

Design only. No server, node, or image has been built, and the commands below are not runnable yet.

A deploy node is not usable until it has pulled an image. The base image is Fedora 44 with systemd, sshd, mise, Go, Rust, and a `box` user. Its Dockerfile is [images/base/Dockerfile](images/base/Dockerfile).

## Fit

Use this when you want exe.dev's "it is just a computer" on hardware you operate: a persistent disk, sudo, a hostname routed to one port, and stock `ssh` for both control and login.

Do not use this as a multi-tenant host. Computers on a node share that node's kernel. The HTTP port is plain and fixed. Put your own edge in front of it. Non-HTTP ports are not published. Computers are not moved between nodes.

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

On the computer, `box portal check` asks whether a hostname is free. `box portal add` claims it for a port inside that container. The server accepts the claim before frp routes it. `key copy` prints the server's GitHub public key and nothing else. A skill in the base image tells an agent how to claim a portal and how to use mise, including a temporary toolchain.

## Design

[DESIGN.md](DESIGN.md) is the spec: binding, the three parts, frp, the REPL, portals, the guest CLI, the base image, and what the first version leaves out.
