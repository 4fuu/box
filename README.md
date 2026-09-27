<p align="center">
  <img src="docs/assets/logo.svg" width="128" alt="box">
</p>

# box

[English](README.md) | [简体中文](README.zh-CN.md)

box puts machines you own behind one SSH entry. A computer is a whole
machine — a workstation, a home server, a VM — not a container. The client is
the `ssh` already installed everywhere. The server is the only public entry.
Computers dial out, so they can sit behind NAT.

`ssh box.example.com` opens the control REPL. `ssh web@box.example.com`
opens a shell on the computer named `web`, at that machine's own sshd.

The reference is [exe.dev](https://exe.dev): machines get names, state
survives, and a site on a machine gets a hostname. This is a private
deployment, not a hosted service.

> [!WARNING]
> This is not a multi-tenant host. The HTTP port serves portals, the event
> API, and a sign-in page, in plain HTTP. TLS, if any, is terminated by an
> edge you run in front of it. This project does not issue certificates. A
> private portal checks a token; it does not encrypt the connection.
> Networks that block UDP cannot run a computer.

## Why box

- **It is the machine.** `scp`, rsync, SFTP, VS Code Remote-SSH, and `ssh -L`
  work because they arrive at a normal sshd. A disconnect does not stop
  anything on the computer.
- **The client is stock OpenSSH.** There is no account and no client to
  install. The first SSH connection that presents the one-time password from
  server init is bound. Later clients get a password from a bound client, or
  from `box pair` on the server.
- **One binary.** `box serve` is the server. `box join`, then `box agent`, is
  the computer. On the computer, `box domain`, `box portal`, and `box event`
  talk to the agent. There is no Podman, no frp, and no node.
- **A hostname for one port.** `box portal add web 3000` claims `web` under
  the domain set when the server starts. The server routes that `Host` to
  `127.0.0.1:3000` on that computer. Add `private` when the portal should
  require a token. An unknown `Host` gets 421. `event.<domain>` and
  `auth.<domain>` belong to the server.
- **Your machines.** A computer joins with a short, single-use approval code.
  It dials out, so it can sit behind NAT.

## Quick start

### Requirements

- a Linux machine for the server, and a Linux machine for each computer
- OpenSSH on the machine you connect from, and sshd on each computer

The server and a computer can be the same machine. The agent does not need root.

### Install

Download the installer and run it in a terminal. It asks for English or
Chinese, then whether this machine is the server or a computer.

```bash
curl -fsSL -o install.sh https://raw.githubusercontent.com/4fuu/box/main/scripts/install.sh
sh install.sh
```

The script installs the `box` binary. It does not install a container runtime
or frp.

A server install can add a systemd unit. Open three ports: SSH (default
`:22`), HTTP (default `:80`), and QUIC (default `:7443`). Then:

```bash
box serve --domain box.example.com
```

The first start prints a one-time password.

A computer install runs `box join <domain>` and can add a systemd user unit
for `box agent`. Approve the printed code at the server.

Releases use a calendar version such as `2026.924.0`. See
[docs/release.md](docs/release.md).

### Start the first session

```bash
ssh box.example.com
```

Present the one-time password from server init. On the computer:

```bash
box join box.example.com
box agent
```

`box join` prints an approval code. Enter it in the server TUI, or run
`approve <code>` from a bound client. The login user is whoever ran
`box join`, unless `--user` names another account on that machine.

`ssh home@box.example.com` is that computer's sshd. The SSH username is the
name registered on the server. The shell runs as the account that ran
`box join` on that machine. The two names do not have to match.

`box join` writes that account's `~/.ssh/box_authorized_keys` and, when it
can, points sshd at that file and turns on `PermitUserEnvironment`. It prints
each change. The account's own `authorized_keys` is left alone.

### Claim a hostname

On the computer:

```bash
box domain
box portal check web
box portal add web 3000
```

`box domain` prints the parent domain, for example `box.example.com`. `web`
becomes `web.box.example.com` and routes to port 3000 on `127.0.0.1`. A label
cannot contain a dot. `event` and `auth` are reserved. `check` does not claim.
`add` refuses when the label is taken. The printed URL has no port.

```bash
box portal add lock 3000 private
```

`private` requires an access token. A browser is sent to `auth.<domain>`,
which asks for the token and stores it in a cookie for the parent domain.
Other clients send `X-Box-Token` or `Authorization: Bearer`. Public portals
stay open. One token opens every private portal for as long as that token is valid.

On the server:

```bash
box token add door
box token ls
box token rm 1
```

`token add` prints the token. `token ls` and the TUI print it again, so it
can be copied later. A token does not expire unless `--for` is set
(`box token add --for 12h door`).

Events are one in-memory log. Devices that are not computers use HTTP.
Computers use `box event`. A bound SSH client uses `event pub` and `event get`.

```bash
box event pub door open
box event get --since 0
```

```bash
curl -H "X-Box-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"topic":"door","body":"open","from":"sensor-1"}' \
  http://event.box.example.com/api/events
curl -H "X-Box-Token: $TOKEN" \
  'http://event.box.example.com/api/events?since=0&topic=door'
```

The log keeps 256 lines and drops the oldest. It is not saved.

`env set NAME <value>` stores a variable and pushes it to online computers.
`env ls` prints names, never values. Access tokens are not env values:
`token ls` prints them.

## Documentation

| Goal | Guide |
| --- | --- |
| Read the spec: binding, the tunnel, the REPL, portals, and what the first version leaves out | [DESIGN.md](docs/DESIGN.md) |
| Cut a dated release | [Release](docs/release.md) |
| See what an agent on a computer is told | [internal/agent/skill/SKILL.md](internal/agent/skill/SKILL.md) |

[DESIGN.md](docs/DESIGN.md) wins when it disagrees with this page.

## Development

The module is Go. Read [AGENTS.md](AGENTS.md) before changing the repository.

```bash
go test ./...
go build -o box .
```

`go test` does not need Podman or frp.

## License

AGPL-3.0-only. The full text is in [LICENSE](LICENSE). Copyright 2026 4fuu
and box contributors. If you run a modified box and let users reach it over a
network, you owe those users the source of your version.
