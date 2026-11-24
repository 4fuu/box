# Architecture

box puts machines you own behind one SSH entry. A computer is a whole machine — a workstation, a home server, a VM — not a container. The client is the stock `ssh`. The server is the only public entry. Computers dial out, so they can sit behind NAT. The reference is [exe.dev](https://exe.dev), as a private deployment rather than a hosted service.

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

The other docs describe each part: [install](install.md), [computers](computers.md), [control](control.md), [portals](portals.md), [events](events.md).

## Ports

Set at first start and stored in the database; later starts reuse the stored values. `--domain` may be repeated: the first domain is the primary, later starts keep the stored list, and any new domains passed on the command line are appended. A single server advertises the primary domain as the QUIC endpoint at join, so every configured domain should resolve to it. In two-node mode `--quic-advertise` names this node's endpoint instead, and the peer port is passed on every start rather than stored.

| Port | Transport | Flag | Default | Use |
| --- | --- | --- | --- | --- |
| SSH | TCP | `--ssh-addr` | `:22` | Client entry, computer bootstrap, REPL |
| HTTP | TCP | `--http-addr` | `:80` | Portals, the event API, blobs, the sign-in page. Plain HTTP |
| QUIC | UDP | `--quic-addr` | `:7443` | Computer tunnels |
| Peer | TCP | `--peer-listen` | none | Two-node link: configuration and cross-node portal forwarding. Private |

The firewall must allow the three public ports. A computer only ever types the SSH address; it learns the QUIC endpoint during the handshake. The peer port is opened only between the two nodes, and is protected by mutual TLS with a pinned certificate.

On the HTTP port, every configured domain serves `event.<domain>` for the event API and `auth.<domain>` for the sign-in page; neither label can be claimed. A private portal's sign-in host and cookie belong to the portal's own domain. Any other unknown `Host` gets 421. TLS, if any, is terminated by an edge the operator runs in front. box issues no certificates, and a private portal's token is an access check, not encryption.

## Two nodes

Two `box serve` processes can share one domain. They are peers for HTTP entry, but only one changes configuration.

| Role | Flag | Does |
| --- | --- | --- |
| writer | `--config-writer` | owns configuration, accepts joins, pushes every change to the replica |
| replica | none | mirrors the writer's configuration, forwards portal claims to the writer, serves with the configuration it has |

Each node keeps its own SQLite file. After every configuration change the writer sends a full snapshot — computers, portals, access tokens, keys, env — at a new revision. The replica applies a snapshot only when its revision is higher, in one transaction. A lost acknowledgement, a crash, or a replica restart costs another copy, never a partial apply. Events and blobs are not in the snapshot.

In two-node mode SQLite syncs each commit to disk. `token rm` reports success only after the replica confirms the snapshot. If confirmation is unavailable, the token is already revoked on the writer and the writer keeps retrying. Until the replica catches up, it may still accept that token. Other configuration writes are replicated asynchronously.

The private link uses the nodes' QUIC certificates, pinned by fingerprint, over mutual TLS. `--peer-listen` is this node's address; `--peer-addr` and `--peer-fingerprint` name the other node. `--quic-advertise` and `--peer-quic` are the public QUIC endpoints handed to computers. Keep the peer port on a private network or behind a firewall.

A computer receives both QUIC endpoints at join and on every hello, races the handshakes, and keeps one tunnel. A portal request that lands on the node without that tunnel is forwarded once to the peer with the original `Host`, escaped path, query, and body. The peer re-checks access and proxies locally; it never forwards again. Responses stream back through the entry, and a request is not replayed after the application may have run. Each node's computer list and `online` flag describe only the tunnels it holds, so a computer attached to the peer appears offline there.

The writer is fixed. If it is down, computers reconnect to the replica and portals already configured keep serving there, but configuration changes wait: joins, portal claims, token changes, key changes, and env changes fail until the writer returns. The replica never promotes itself; with two nodes there is no partition-safe arbitrator. Joins must reach the writer, so point `box join` at the writer's hostname.

SSH entry is not forwarded: `ssh computer@<domain>` works only when the request reaches the node holding that computer's tunnel. Events and blobs belong to the node that received the request. Put a load balancer or CDN in front if one name must reach both nodes; box does not run one.

## The tunnel

Every persistent byte between server and computer travels one QUIC connection the computer dials. Streams are independent, so a stalled portal request never blocks an SSH session. TLS 1.3 is the only encryption. The server's certificate is self-signed at first start and pinned by the computer at join. There is no CA and no renewal.

| Stream | Opened by | Carries |
| --- | --- | --- |
| Control | computer, once per connection | newline-delimited JSON: hello, portal claim/check/release, key pushes, stat replies, event publish and fetch |
| `ssh` | server, per client session | raw TCP to the machine's own sshd |
| `portal` | server, per HTTP request | raw TCP to 127.0.0.1 and the claimed port |

A server-opened stream starts with a small header naming its type and target, then passes bytes unchanged. A client's SSH handshake runs end-to-end with the computer's sshd; the server cannot read it.

The first control frame is the hello: protocol version, name, token, login user, sshd host public key. An unknown version or token is refused. Online **is** the tunnel — no heartbeat table, no staleness window. When the tunnel drops, routes go; when it returns, they rebuild from the database. Every reconnect repeats the SSH bootstrap first, so moving the server or its QUIC port needs no change on any computer. The agent backs off from 1 to 30 seconds with jitter.

## SSH entry

The server speaks SSH with [charmbracelet/wish](https://github.com/charmbracelet/wish). The username and the key are the route. Auth is public-key only, so an unknown key fails with `Permission denied (publickey)`. The pairing password is typed inside an already-keyed session; the join hash and the computer token ride the first session line.

| Username | Auth | Result |
| --- | --- | --- |
| empty, or `box` | bound key | control REPL |
| empty, or `box` | unknown key, only while a pairing password is live | bind this key at an in-session prompt |
| `pair+<password>` | any key | bind this key, non-interactively |
| `join+<name>` | any unbound key; code hash as first line | park a pending join |
| `boot+<name>` | any unbound key; computer token as first line | tunnel bootstrap for the agent |
| a computer's name | bound key | splice to that computer's sshd |
| any other name | bound key | control REPL |

Outside those pairing flows an unbound key is rejected at auth. A computer name cannot be `box`, `pair`, or `join`, and cannot contain `+` or `.`. Names are globally unique.

A connection may offer three keys and no more. A client address that fails five times in five minutes is refused for five minutes, and each further ban doubles up to thirty. A refused address is dropped when it connects, before the key exchange, so a guessing client costs an accept and a close and nothing is written about it: failures are not logged, which keeps a flood from filling the disk.

Because the splice ends at a normal sshd, `scp`, `rsync`, SFTP, VS Code Remote-SSH, and `ssh -L` work. A disconnect stops nothing on the computer.

## Processes

| Machine | Process | Listens |
| --- | --- | --- |
| Server | `box serve` | SSH, HTTP, QUIC, and a localhost socket |
| Computer | `box join`, then `box agent` | nothing public; a unix socket in `~/.box` |

`box agent` reads `~/.box/computer.json` and reconnects. It is meant to run under a systemd user unit, or on Windows as a scheduled task at logon.

## Data

One SQLite file in the data directory (default `/var/lib/box`, `%ProgramData%\box` on Windows, `--data-dir`). It is the secret store and is not world-readable. The file runs in WAL journal mode with `synchronous=NORMAL`.

- `keys`: public key, comment, bound at
- `pairings`: hash of a one-time password, expiry, used at, failed attempts
- `computers`: name, token hash, login user, sshd host public key, joined at
- `portals`: hostname, computer, port, private (default false), claimed at
- `tokens`: access token, comment, expiry, created at. The value is stored so it can be copied again
- `env`: name, value. List commands never return the value
- `events`: id (AUTOINCREMENT, int64), topic, body (any bytes), from label, token id, dedup key, created at. Indexed on (topic, id) and (from, id); a partial unique index on (from, dedup key) makes dedup race-free. At most 100000 rows, none older than 7 days
- `blobs`: id (AUTOINCREMENT, int64), size, content type, from label, token id, backend, created at. Indexed on created_at. At most 4 GiB together, none older than 7 days; the bytes live in the backend, not in this file
- `meta`: domains (the primary is also kept as `domain`), the three listen addresses, server keys, schema version

Pending joins and TUI counters live in memory only. The agent keeps `~/.box/computer.json`: token, QUIC endpoint, server fingerprint, name, login user.

## Dependencies

| Piece | Choice | Why |
| --- | --- | --- |
| Client | OpenSSH, already installed | No client to ship; Windows 10+ includes the client |
| Control SSH | charmbracelet/wish | An sshd we can branch on username and auth method |
| TUI | bubbletea + lipgloss | wish's companion; one framework for REPL and dashboard |
| Tunnel | quic-go | Independent streams, one TLS stack, NAT rebinding, connection stats |
| Server state | SQLite | One file, enough for a private deployment |
| Computer SSH | the machine's openssh-server | A normal sshd, so scp and Remote-SSH work; on Windows the OpenSSH Server optional capability |

## Not in the first version

- accounts, billing, invites, teams, SSO, email
- a web coding agent
- per-computer key scoping
- a TCP fallback for the tunnel; networks that block UDP cannot run a computer
- TLS on the HTTP port
- moving a computer's identity between machines
- resource limits on a computer
- CDN
