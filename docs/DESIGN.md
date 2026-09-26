# Design

A self-hosted way to put machines you own behind one SSH entry. A computer is a whole machine — a workstation, a home server, a VM. It is not a container. The operator's client is the `ssh` already installed everywhere. The server is the only public entry. Computers dial out, so they can sit behind NAT.

```diagram
client                      server                         computer
OpenSSH                     public SSH entry               operator's machine

ssh box.example.com         control REPL
ssh web@box.example.com     username is the computer       box agent (no root)
                            HTTP port, route by Host
                            QUIC listener (UDP)
                                     │
                                     │ computer dials out:
                                     │ SSH bootstrap, then QUIC tunnel
                                     ▼
                            commands ──────────────▶  agent
                            SSH splice ────────────▶  the machine's own sshd
                            HTTP by Host ──────────▶  127.0.0.1:port on the machine
```

The reference is [exe.dev](https://exe.dev): machines get names, state survives, a website on a machine gets a hostname. This is a private deployment, not a hosted service.

## Status

This document is the source of truth. The README is the short version. When they disagree, follow this document.

This revision replaces the container design. There is no Podman, no image, no node/controller split, and no frp. A node **is** a computer. The data plane is a QUIC tunnel the computer opens to the server.

## Ports

The server listens on three ports. All three are configurable at first start and stored in the server's database; later starts reuse the stored values.

| Port | Transport | Flag | Default | Use |
| --- | --- | --- | --- | --- |
| SSH | TCP | `--ssh-addr` | `:22` | Client entry, computer bootstrap, REPL |
| HTTP | TCP | `--http-addr` | `:80` | Portals only. Plain HTTP for the operator's edge |
| QUIC | UDP | `--quic-addr` | `:7443` | Computer tunnels |

The operator's firewall must allow all three. Computers never configure a port: they connect to the SSH port, which is the one address a person types, and learn the QUIC endpoint during the handshake.

The HTTP port serves portals and nothing else. There is no HTTP API. An unknown `Host` gets 421. TLS, if any, is terminated by an edge the operator runs in front of this port. This project does not issue certificates.

## Binding clients

There is no account. Trust is a public key accepted by the server.

When the server is initialized it prints a one-time password and its expiry. The first SSH connection that presents the password is bound: the server stores that client's public key and then refuses the password. Expiry also refuses it. The password is never written to the client.

To bind another client, either:

- from an already bound client, run `pair`, which prints a new one-time password, or
- on the server machine, run `box pair`.

A bound key can be listed and removed from a bound client, or from the localhost CLI. Removing the last key does not unlock the server. Initialize again, or pair from localhost.

Any bound key may reach any computer. Per-computer key scoping is not in the first version.

## Adding a computer

A computer joins with a short approval code, verified by a person at the server. The code confirms the machine is the one the operator intends to add.

```
me@home:~$ box join box.example.com
approval code: a3Kf9Q
enter this code at the server to approve "home"
waiting…
approved. tunnel up as home.box.example.com
```

1. The computer generates a 6-character code (mixed-case letters and digits) and prints it. It opens an SSH connection to the server — just the domain, default port 22, or `domain:port` — with the username `join+<name>`. No key and no password is required to park this session. The computer sends a hash of the code, not the code, and waits.
2. The server adds a pending join: name, remote address, code hash, expiry 10 minutes. It appears in the server TUI and in the REPL `pending` list.
3. A person approves it: enter the code in the TUI, or run `approve <code>` from a bound client. Five wrong attempts discard the pending join. Attempts are rate-limited per remote address.
4. On approval the server generates a computer token and replies over the waiting SSH session: the token, the QUIC endpoint, and the server's QUIC certificate fingerprint. The computer stores them in `~/.box/computer.json`, mode 0600.
5. The computer then runs its agent: it opens the QUIC tunnel and stays up.

The code is single use and never logged. Approval requires an already-bound key or access to the server console, so an attacker who can only reach the SSH port cannot approve their own machine.

`rm <name>` on the server deletes the computer's record and revokes its token. The agent's next reconnect is rejected; it wipes `~/.box` and exits. Rejoining repeats the approval ceremony.

## The tunnel

Every persistent byte between the server and a computer travels one QUIC connection the computer dials out. QUIC gives independent streams, so a stalled portal request never blocks an SSH session, and a single TLS 1.3 stack is the only encryption.

The server's QUIC certificate is self-signed, generated at first start and stored next to the database. The computer pins its fingerprint at join time, delivered over the already-approved SSH session. There is no CA and no certificate renewal.

Streams:

| Stream | Opened by | Carries |
| --- | --- | --- |
| Control | computer, once per connection | newline-delimited JSON frames: hello, portal claim/check/release, key and env pushes, stat replies |
| `ssh` | server, per client session | raw TCP splice. The agent connects it to the machine's own sshd |
| `portal` | server, per HTTP request | raw TCP. The agent connects it to 127.0.0.1 and the claimed port |

A server-opened stream begins with a small header naming its type and target; after the header the bytes pass through unchanged. The SSH handshake of a client runs end-to-end between the client and the computer's sshd. The server cannot read it.

The control stream's first frame is the agent's hello: protocol version, computer name, token, login user, and the sshd host public key. The server refuses a protocol version it does not understand and an unknown token. Online **is** the tunnel: a computer is online exactly while its QUIC connection lives. There is no heartbeat table and no staleness window. When the tunnel drops, the server tears down its routes; when the agent reconnects, routes rebuild from the database.

Reconnect always repeats the full bootstrap: SSH handshake first, which returns the current QUIC endpoint, then the tunnel. Moving the server or changing its QUIC port needs no change on any computer. The agent backs off from 1 second to 30 with jitter.

## SSH entry

The server speaks SSH with [charmbracelet/wish](https://github.com/charmbracelet/wish). SSH has no `Host` header, so the username and the auth method together are the route.

| Username | Auth | Result |
| --- | --- | --- |
| empty, or `box` | bound key | control REPL |
| empty, or `box` | unknown key + live password prompt | bind this key |
| `pair+<password>` | any key | bind this key, non-interactively |
| `join+<name>` | none | park a pending computer join |
| a computer's name | that computer's token | tunnel bootstrap for the agent |
| a computer's name | bound client key | splice to that computer's sshd |

A computer name cannot be `box`, `pair`, or `join`, and cannot contain `+` or `.`. Names are globally unique, so the username alone is enough.

The splice is end-to-end: the client's handshake finishes at the computer's sshd against the keys the server pushed there. `scp`, `rsync`, SFTP, VS Code Remote-SSH, and `ssh -L` work because they arrive at a normal sshd. A disconnect does not stop anything on the computer.

## A computer

The agent runs as a regular user. It never needs root. The login user is the user who ran `box join`, unless `--user <name>` names an existing account on that machine.

The agent manages one file, `~/.ssh/box_authorized_keys`, and nothing else in the account. The server's bound client keys are written there and kept in sync over the control stream. The user's own `authorized_keys` is never touched. sshd must list the managed file as an additional `AuthorizedKeysFile`; `box join` adds it when it can and otherwise prints the one line for the operator.

Environment variables set with `env set` are injected per key, using OpenSSH's `environment="NAME=value"` authorized-keys option. No sshd configuration change, no `PermitUserEnvironment`. An online computer receives pushes as they happen; a new SSH session sees the current set. `env ls` prints names, never values.

The agent also writes the box skill to `~/.agents/skills/box/SKILL.md` so an agent on the computer knows how to claim portals. The skill contains no credentials.

Inside a computer, `box` is the guest CLI talking to the agent's unix socket at `~/.box/agent.sock`:

```
box domain
box.example.com
box portal check web
free
box portal add web 3000
http://web.box.example.com
```

## Portals

A portal is a hostname routed to one TCP port on one computer. The computer claims it; the control REPL does not.

The parent domain is set when the server starts. `box domain` prints it. `check` and `add` take a label, never a full hostname; the server joins the label to the domain. A label cannot contain a dot, so a computer cannot claim a name outside that domain. `add` claims atomically or refuses and names the holder. A computer can hold several labels; each points at one port. A portal hostname is globally unique and is not derived from the computer's name.

The claim travels the control stream. The server is the only place that knows every claim. Once accepted, an HTTP request arriving at the server's HTTP port with that `Host` opens a `portal` stream to the computer, and the agent connects it to `127.0.0.1:<port>` — so the process only needs to listen on loopback. The route exists before anything listens; the operator sees a connection error until it does. When the printed URL's port is not 80, it is included in the URL.

Removing a computer drops its portals. The claim record lives on the server; the route lives only while the tunnel lives.

## Control REPL and TUI

The control plane is the SSH command itself, plus a REPL for a person. Interactive sessions are a full TUI built with bubbletea: tables for lists, forms for approval, color for state. Non-interactive sessions (`ssh box.example.com ls`) print plain text; `--json` is for scripts. Both share one service layer.

| Command | Effect |
| --- | --- |
| `ls` | Computers: name, online, user, address, agent version, portals |
| `ssh <name>` | Open a shell on that computer |
| `rm <name>` | Delete the computer and revoke its token. Ask for the name again |
| `rename <name> <new>` | Rename. Portals stay claimed; the SSH username changes |
| `stat <name>` | Live load from the agent: cpu, memory, disk, uptime |
| `pending` | Computers waiting for approval |
| `approve <code>` | Approve a pending join |
| `pair` | Print a one-time password for another client |
| `key ls` / `key rm` | List or remove bound client keys |
| `env set <name> <value>` | Store a variable and push it to online computers |
| `env rm <name>` | Remove it. Existing sessions keep the old value |
| `env ls` | List names only |
| `whoami` | Which key this session used |

On the server machine, `box` with no arguments opens the server TUI over the localhost socket: computers and their live state, the pending-join queue with an approval form, portals, keys, and env names. It can approve, remove computers, and remove keys. It cannot bind a client key for itself — that stays on the SSH password path, so a person on the console cannot skip the key check by accident.

## Processes

One process per machine. There is no frps, no Podman, no second binary.

| Machine | Process | Listens |
| --- | --- | --- |
| Server | `box serve` | SSH, HTTP, QUIC, and a localhost socket |
| Computer | `box join`, then `box agent` | nothing public; a unix socket in `~/.box` |

`box agent` reads `~/.box/computer.json` and reconnects. It is meant to run under a systemd user unit so it starts at boot without root.

## Data

The server keeps one SQLite file in its data directory (default `/var/lib/box`, configurable with `--data-dir`).

- `keys`: public key, comment, bound at
- `pairings`: hash of a one-time password, expiry, used at, failed attempts
- `computers`: name, token hash, login user, sshd host public key, joined at
- `portals`: hostname, computer, port, claimed at
- `env`: name, value. The value is not returned by list commands
- `meta`: domain, the three listen addresses, server keys, schema version

Pending joins live in memory only. The SQLite file is the secret store; it is not world-readable. Computer names and portal hostnames are globally unique.

The agent keeps `~/.box/computer.json`: token, QUIC endpoint, server fingerprint, name, login user. The server does not store per-computer files.

## Install and deploy

One release archive per OS and architecture, containing the single `box` binary. The install script (`scripts/install.sh`) downloads and verifies it, then asks one question: server or computer.

- **Server**: install the binary, optionally a systemd unit, then `box serve --domain box.example.com`. Open the three ports in the firewall. The first start prints the one-time password.
- **Computer**: install the binary, run `box join <domain>`, approve the code at the server, optionally install a systemd user unit for `box agent`.

No container runtime, no image, no frp download, no cgroup check.

## Not in the first version

- accounts, billing, invites, teams, SSO
- email
- a web coding agent
- per-computer key scoping; every bound key reaches every computer
- a TCP fallback for the tunnel. Networks that block UDP cannot run a computer
- TLS and public access control on the HTTP port
- moving a computer's identity between machines
- resource limits on a computer; it is a whole machine
- CDN

## Dependencies

| Piece | Choice | Why |
| --- | --- | --- |
| Client | OpenSSH, already installed | No client to ship |
| Control SSH | charmbracelet/wish | An sshd we can branch on username and auth method |
| TUI | bubbletea + lipgloss | wish's native companion; one framework for REPL and dashboard |
| Tunnel | quic-go | Independent streams, one TLS stack, NAT rebinding, no extra daemon |
| Server state | SQLite | One file, enough for a private deployment |
| Computer SSH | the machine's openssh-server | A normal sshd, so scp and Remote-SSH work |

The `box` binary, the server, the agent, and the skill are the code to write. The rest is the software above.
