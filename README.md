# box

Persistent Linux computers on your own machines, reached with the `ssh` you already have.

`ssh box.example.com` opens a control REPL. `new` creates a computer on a deploy node you paired. `ssh vm+web@box.example.com` opens a shell on that computer. A fixed HTTP port on the server routes `Host` to the computer, so an edge you run can publish it. The node may sit behind NAT. [frp](https://github.com/fatedier/frp) carries commands, SSH, and HTTP between the server and the node.

There is no account. The server prints a one-time password at init. The first SSH client to present it is bound. Further clients get a password from a bound client, or from a localhost command on the server. A node joins with a short pairing code.

## Status

Design only. No server, node, or image has been built, and the commands below are not runnable yet.

A deploy node is not usable until it has pulled an image. The base image is a Debian computer with systemd, a `box` user, and a guest CLI. Its Dockerfile is [images/base/Dockerfile](images/base/Dockerfile).

## Fit

Use this when you want exe.dev's "it is just a computer" on hardware you operate: a persistent disk, root, `apt`, a hostname routed to one port, and stock `ssh` for both control and login.

Do not use this as a multi-tenant host, a TLS terminator, or a general TCP gateway. The HTTP port is plain and fixed. Put your own edge in front of it. Non-HTTP ports are not published. Computers are not moved between nodes.

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

`new` prints `ssh vm+web@box.example.com`. That destination is what `scp` and VS Code Remote-SSH use.

On the computer, `box http` and `box domain` ask the node controller to change the routed port or register a hostname. The server accepts the name before frp routes it. A skill in the base image tells an agent on the computer to use those commands.

## Design

[DESIGN.md](DESIGN.md) is the spec: binding, the three parts, frp, the REPL, HTTP routing, the guest CLI, the base image, and what the first version leaves out.
