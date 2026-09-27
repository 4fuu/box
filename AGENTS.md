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
| `box serve --domain <domain>` | server | SSH entry, HTTP portals, QUIC tunnels, SQLite, localhost socket |
| `box join <host>`, then `box agent` | computer | approval, then the QUIC tunnel and `~/.box/agent.sock` |
| `box domain`, `box portal …` | computer | guest CLI. Talks only to the agent socket |

The server listens on three ports: SSH, HTTP, and QUIC. Computers dial out. The SSH username is the computer name. An empty username, or `box`, opens the control REPL. `web` is spliced to the computer named `web`, and the session ends at that machine's sshd.

A portal is a label the computer claims, joined to the domain configured at server start. Portals are HTTP only. The computer reads the domain with `box domain`. It does not invent one. The process listens on `127.0.0.1`.

`box serve` and `box agent` are not refused because an agent socket exists. The server runs `box serve`. A computer runs `box agent`.

## Do not add

The first version leaves these out. Do not start them:

- accounts, billing, invites, teams, SSO, email
- a web coding agent
- per-computer key scoping; every bound key reaches every computer
- a TCP fallback for the tunnel
- TLS or certificate issuance, and public access control on the HTTP port
- moving a computer's identity between machines
- resource limits on a computer; it is a whole machine
- CDN
- Podman, frp, a node/controller split, or a container runtime
- a second binary

## Secrets

`env ls` prints names, never values. Pairing passwords and approval codes are single use. Do not log them, and do not log computer tokens, private keys, or env values. The computer token stays in `~/.box/computer.json`, mode 0600.

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
