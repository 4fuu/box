# box

Persistent Linux computers on hardware the operator owns. A computer is a machine, not a container. The client is stock `ssh`. One `box` binary is the server, the agent, and the guest CLI. [DESIGN.md](docs/DESIGN.md) is the spec. [README.md](README.md) is the short version. When they disagree, follow DESIGN.md.

## Status

The Go module implements the server, the computer agent, and the guest commands in one `box` binary. Do not invent commands, flags, or packages that DESIGN.md does not name. `go test` does not need Podman or frp. There is no base image.

Build and test with:

```
go test ./...
go build -o box .
```

`.agents/setup` installs Go. The `toolchain` or `go` line in `go.mod` is the pin.

## Shape

One executable. The subcommand selects the role. There is no `boxd`, no `box-node`, no Podman, and no frp.

| Command | Where | Does |
| --- | --- | --- |
| `box serve --domain <domain>` | server | SSH entry, HTTP portals, event API, sign-in page, QUIC tunnels, SQLite, localhost socket |
| `box join <host>`, then `box agent` | computer | approval, then the QUIC tunnel and `~/.box/agent.sock` |
| `box domain`, `box portal …`, `box event …` | computer | guest CLI. Talks only to the agent socket |
| `box token …`, `box event …` | server | localhost socket. `box token` creates access tokens. On a computer, `box event` is the guest command above |

The server listens on three ports: SSH, HTTP, and QUIC. Computers dial out. The SSH username is the computer's registered name. The session account is whoever ran `box join` on that machine; the two names do not have to match. An empty username, or `box`, opens the control REPL. `web` is spliced to the computer named `web`, and the session ends at that machine's sshd. `box join` updates sshd when the config is writable, prints each change, and reloads sshd when it can.

A portal is a label the computer claims, joined to the domain configured at server start. Portals are HTTP only. The computer reads the domain with `box domain`. It does not invent one. The process listens on `127.0.0.1`. `box portal add <label> <port>` is public. `box portal add <label> <port> private` requires an access token. Labels `event` and `auth` are reserved: `event.<domain>` is the event API, `auth.<domain>` is the sign-in page. Events are a durable log in the server's SQLite file: at most 100000 rows, none older than 7 days, filterable and long-pollable. Computers publish with `box event`. Devices that are not computers `POST` and `GET` `http://event.<domain>/api/events` with the token.

`box serve` and `box agent` are not refused because an agent socket exists. The server runs `box serve`. A computer runs `box agent`.

## Do not add

The first version leaves these out. Do not start them:

- accounts, billing, invites, teams, SSO, email
- a web coding agent
- per-computer key scoping; every bound key reaches every computer
- a TCP fallback for the tunnel
- TLS or certificate issuance. Private portals check a token; they do not encrypt HTTP
- moving a computer's identity between machines
- resource limits on a computer; it is a whole machine
- CDN
- Podman, frp, a node/controller split, or a container runtime
- a second binary

## Secrets

`env ls` prints names, never values. Pairing passwords and approval codes are single use. Do not log them, and do not log computer tokens, access tokens, private keys, or env values. Access tokens are stored in SQLite and shown again by `token ls` and the TUI, because the operator copies them more than once. The computer token stays in `~/.box/computer.json`, mode 0600. The event API and private portals use access tokens, not the computer token.

## Repo

| Path | What it is |
| --- | --- |
| `docs/DESIGN.md` | Spec |
| `internal/agent/skill/SKILL.md` | Skill embedded into the agent and written on the computer. No credentials |
| `.agents/setup` | Orb toolchain install |
| `.agents/resume` | Checks that Go is still present. Does not install anything |
| `scripts/install.sh` | Interactive Linux installer. Asks: server or computer |
| `VERSION` | Calendar version `YYYY.MDD.REVISION` |
| `LICENSE` | AGPL-3.0-only |
| `.github/workflows/ci.yml` | Test and installer dry run |
| `.github/workflows/release.yml` | Date-version release of Linux archives |

Git commit messages and branch names are English.
