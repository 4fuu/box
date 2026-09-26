# Refactor plan

This document maps the current container-based implementation onto the design in [DESIGN.md](DESIGN.md) (node-is-computer, QUIC tunnel, SSH bootstrap, approval codes, TUI). It is a working plan, not a spec: DESIGN.md wins when they disagree. Everything here is a breaking change; the project has no released state to migrate, so schemas and commands are replaced, not migrated.

## What goes away

| Existing | Fate |
| --- | --- |
| `internal/runtime/` (Podman) | Delete |
| `internal/frp/` | Delete |
| `internal/api/` (HTTP node API) | Delete; portal claim/check/release move to the control stream |
| `images/` (base image, Dockerfile, skill source) | Delete. The skill moves to `internal/agent` and is embedded with `go:embed` |
| `internal/node/` | Rewritten as `internal/agent` |
| `internal/repl/` | Rewritten on bubbletea; the hand-rolled line editor goes |
| `internal/size/` | Delete; no cpu/memory/disk sizing |
| REPL commands `new`, `node *`, `image *`, `resize`, `restart`, `defaults` | Delete |
| `key copy` and the server GitHub key | Delete. The splice is end-to-end now, so the server needs no key of its own |
| store tables `nodes`, `images`, `shares`, computer size/state fields | Delete. No migration |
| frps/frpc, Podman, cgroup steps in `scripts/install.sh` | Delete |
| `paths.go` constants `SSHListen`, `HTTPListen`, `FRPPort`, `FRPVHostPort`, `SSHDPort`, `LoginUser` | Replaced by stored configuration |

## What stays

- `internal/store` — slimmed to the schema in DESIGN.md (keys, pairings with attempt counters, computers, portals, env, meta)
- `internal/rpc` — the newline-JSON frame format, reused on the control stream
- `internal/keys`, `internal/ident`, `internal/secret` — unchanged in purpose; `secret` gains the 6-character mixed-case code
- `internal/server` — kept as a package, heavily reworked (see below)
- The wish-based SSH entry and its password-binding flow for clients — unchanged in behavior
- The guest CLI surface (`box domain`, `box portal …`) — unchanged for users

## New pieces

| Package | Responsibility |
| --- | --- |
| `internal/tunnel` | QUIC listener (server) and dialer (agent), TLS with pinned self-signed cert, stream headers (`ssh`, `portal`), control-stream framing, version negotiation, reconnect with backoff |
| `internal/agent` | `box join` bootstrap, `box agent` loop, `~/.box` state, authorized-keys writer with `environment=` injection, guest socket, portal and sshd stream handling, skill installation |
| `internal/approve` | Pending-join queue: code hash, expiry, attempt counter, per-address rate limit; shared by the TUI and the REPL |
| `internal/tui` | bubbletea components: computer table, approval form, portal/key/env views. Used by the server dashboard and the interactive REPL |

## Server rework

- `server.Config` takes `SSHAddr`, `HTTPAddr`, `QUICAddr`; all three persist to `meta` on first start, like the domain does today
- `ssh.go` branches on username **and** auth method: REPL (bound key), password binding, `join+<name>` pending sessions, token-authenticated agent bootstrap, key-authenticated computer splice. One table drives this; it is the security-critical spot of the codebase
- The HTTP listener keeps only Host routing and 421; `nodeAPI`, `authNode`, `join`, `heartbeat`, `claim`, `check`, `release` are deleted
- Portal routing dials a `portal` stream on the computer's live tunnel instead of proxying to frps
- The splice dials an `ssh` stream; the end-to-end handshake and host-key pinning (key reported in the agent hello) stay as designed
- Online state is the live tunnel map; `Heartbeat`, `OnlineFor`, and staleness logic are deleted
- The localhost unix socket keeps the local CLI and now also feeds the TUI

## Agent notes

- Runs as the login user; never root. State in `~/.box`, socket at `~/.box/agent.sock`
- Every connection starts with the SSH bootstrap (`<name>` + token) to learn the current QUIC endpoint, then dials the tunnel
- Writes only `~/.ssh/box_authorized_keys`; env values become `environment="K=V"` options on those lines
- A rejected token (after `rm`) means: wipe `~/.box` and exit
- Reports load for `stat` from `/proc`

## Work order

1. `docs/DESIGN.md` rewrite (done, this change) and this plan
2. `internal/store` slim-down, new schema, tests
3. `internal/tunnel` with loopback tests: stream types, bad token, wrong version, reconnect
4. `internal/server`: config persistence, SSH branching, approval queue, portal routing, splice
5. `internal/agent`: join, tunnel loop, keys/env writer, guest socket, portal/ssh stream handlers
6. `internal/tui` + REPL rewrite; plain-text and `--json` paths preserved for scripts
7. `internal/cli` wiring, `scripts/install.sh` rewrite, README and README.zh-CN, AGENTS.md
8. Delete the dead packages and `images/`; full `go test ./...` and an end-to-end run: server + two computers + portal + scp, all on loopback

## Known gaps this refactor closes

These exist in the current tree and are fixed by the rewrite rather than patched:

- The server-side frp visitor dial was never wired; `Config.Dial` is only set in tests, so the REPL could never actually reach a computer
- `box serve` exposed no port flags even though `server.Config` had the fields
- `node join` took a frps address (`host:7000`) while the HTTP API assumed port 80 on the same host
- The HTTP node API shared port 80 with portals, separated only by the `Host` header
