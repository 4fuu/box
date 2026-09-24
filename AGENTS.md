# box

Persistent Linux computers on hardware the operator owns. The client is stock `ssh`. One `box` binary is the server, the deploy-node controller, and the guest CLI. [DESIGN.md](docs/DESIGN.md) is the spec. [README.md](README.md) is the short version. When they disagree, follow DESIGN.md.

## Status

The Go module implements the three roles in one `box` binary. Do not invent commands, flags, or packages that DESIGN.md does not name. `go test` does not build the base image and does not need Podman or frp.

Build and test with:

```
go test ./...
go build -o box .
```

`.agents/setup` installs Go. The `toolchain` or `go` line in `go.mod` is the pin.

## Shape

Three roles, one executable. The subcommand selects the role. There is no `boxd` and no `box-node`.

| Command | Where | Does |
| --- | --- | --- |
| `box serve` | server | SSH entry, HTTP routing, SQLite, pairing and env store |
| `box node` | deploy node | controller: frpc, Podman, guest socket |
| `box` | inside a container | `domain` and `portal` only. Refuses `serve` and `node` |

The SSH username is the container name, not a login user. The login user inside a container is `box`. An empty username, or `box`, opens the control REPL. `web` is spliced to the container named `web`.

A portal is a label the container claims, joined to the domain configured at server start. The container reads that domain with `box domain`. It does not invent one, and it does not edit frp config.

## Do not add

The first version leaves these out. Do not start them:

- accounts, billing, invites, teams, SSO, email
- a web coding agent
- a public IP or hardware VM per computer
- TLS or certificate issuance
- moving a computer between nodes
- a second container runtime behind Podman
- a second binary

Podman is rootful. Containers are not privileged, do not use the host network, and do not receive a Docker socket. The image Dockerfile copies a released `box` binary. It does not compile one.

## Secrets

`env ls` prints names, never values. `key copy` prints the server's GitHub public key and nothing else. The private key stays on the server. Pairing passwords and node codes are single use. Do not log them, and do not write them into the image or a volume.

## Repo

| Path | What it is |
| --- | --- |
| `docs/DESIGN.md` | Spec |
| `images/base/Dockerfile` | Fedora 44 base computer. Not built by `go test` |
| `images/base/skills/box/SKILL.md` | Skill copied into the image for an agent inside a computer |
| `.agents/setup` | Orb toolchain install |
| `.agents/resume` | Checks that Go is still present. Does not install anything |
| `scripts/install.sh` | Interactive Linux installer for a server or a deploy node |
| `VERSION` | Calendar version `YYYY.MDD.REVISION` |
| `.github/workflows/ci.yml` | Test and installer dry run |
| `.github/workflows/release.yml` | Date-version release of Linux archives and the base image |

Git commit messages and branch names are English.
