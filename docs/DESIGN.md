# Design

This document is the source of truth. The README is the short version; when they disagree, follow this document.

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

## Ports

Set at first start and stored in the database; later starts reuse the stored values.

| Port | Transport | Flag | Default | Use |
| --- | --- | --- | --- | --- |
| SSH | TCP | `--ssh-addr` | `:22` | Client entry, computer bootstrap, REPL |
| HTTP | TCP | `--http-addr` | `:80` | Portals, the event API, the sign-in page. Plain HTTP |
| QUIC | UDP | `--quic-addr` | `:7443` | Computer tunnels |

The firewall must allow all three. A computer only ever types the SSH address; it learns the QUIC endpoint during the handshake.

On the HTTP port, `event.<domain>` is the event API and `auth.<domain>` is the sign-in page; neither label can be claimed. Any other unknown `Host` gets 421. TLS, if any, is terminated by an edge the operator runs in front. box issues no certificates, and a private portal's token is an access check, not encryption.

## Binding clients

There is no account. Trust is a public key the server accepted.

At init the server prints a one-time password valid for five minutes. While it is live, the first client to connect with any key gets a password prompt inside the session; typing it binds that client's key, after which the password is refused. The password is never written to the client.

To bind another client, run `pair` from a bound client, or `box pair` on the server machine. Keys are listed and removed from a bound client or the localhost CLI. Removing the last key does not unlock the server; initialize again or pair from localhost. Every bound key reaches every computer.

## Adding a computer

A computer joins with a short approval code that a person verifies at the server.

```
me@home:~$ box join box.example.com
approval code: a3Kf9Q
enter this code at the server to approve "home"
waiting…
approved. tunnel up as home.box.example.com
```

1. The computer prints a 6-character code (mixed-case letters and digits), connects over SSH (domain, default port 22, or `domain:port`) as `join+<name>` with a fresh key pair, sends a hash of the code as the first session line, and waits. Machine keys are never bound.
2. The server adds a pending join — name, remote address, code hash, 10-minute expiry — shown in the TUI and the REPL `pending` list.
3. A person approves: the code in the TUI, or `approve <code>` from a bound client. Five wrong attempts discard the join. Attempts are rate-limited per remote address.
4. The server replies on the waiting session with a computer token, the QUIC endpoint, and its QUIC certificate fingerprint. The computer stores them in `~/.box/computer.json`, mode 0600, and starts its agent.

The code is single use and never logged. Approval needs a bound key or the server console, so reaching the SSH port is not enough to approve your own machine.

`rm <name>` deletes the record and revokes the token. The agent's next reconnect is rejected; it wipes `~/.box` and exits. Rejoining repeats the approval.

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

Because the splice ends at a normal sshd, `scp`, `rsync`, SFTP, VS Code Remote-SSH, and `ssh -L` work. A disconnect stops nothing on the computer.

## A computer

The agent runs as a regular user and never needs root. The login user is whoever ran `box join`, unless `--user <name>` names another existing account. `ssh home@box.example.com` opens a shell as that account on the computer registered as `home`; the two names need not match.

The agent manages one file, `~/.ssh/box_authorized_keys`, kept in sync with the server's bound keys. The user's own `authorized_keys` is untouched. `box join` adds the file to sshd's `AuthorizedKeysFile` when it can write the config, prints each change, and reloads sshd when it can; otherwise it prints the lines and the reload command.

`env set` values live on the server only. Each splice session receives the current set as SSH env requests, so values exist on the computer only in that session's memory. sshd drops names outside `AcceptEnv`, so `box join` adds `AcceptEnv *` when no global `AcceptEnv` line exists and says so; existing lines are left alone. `env ls` prints names, never values.

The agent writes the box skill to `~/.agents/skills/box/SKILL.md`. It holds no credentials.

On a computer, `box` is the guest CLI on the agent socket `~/.box/agent.sock`:

```
box domain
box.example.com
box portal check web
free
box portal add web 3000
http://web.box.example.com
box portal add lock 3000 private
box event pub door open
box event get --since 0
box event get --follow --topic door
```

## Portals

A portal is a hostname routed to one TCP port on one computer. The computer claims it; the REPL does not.

`check` and `add` take a label; the server joins it to the domain set at server start (`box domain` prints it). A label cannot contain a dot; `event` and `auth` are reserved. `add` claims atomically or refuses and names the holder. A computer can hold several labels, each on one port. Hostnames are globally unique and not derived from the computer's name.

`box portal add <label> <port>` is public; appending `private` requires an access token. Claiming the label again changes that. Existing rows stay public.

A request with a claimed `Host` opens a `portal` stream, which the agent connects to `127.0.0.1:<port>`, so the process only needs loopback. The route exists before anything listens. The printed URL carries no port; the edge decides the reachable one.

A private portal checks the token before opening the stream. A browser navigation (`Accept` contains `text/html`) without one is redirected to `http://auth.<domain>/`, which sets an HttpOnly cookie on the parent domain once a valid token is submitted. Other clients send `X-Box-Token` or `Authorization: Bearer` and get 401 without it. The server strips that cookie, and a bearer token that is one of its access tokens, before forwarding. One token opens every private portal.

Removing a computer drops its portals. Claims live on the server; routes live while the tunnel does.

## Events

A durable log in the server's SQLite file. An event has an id, topic, body, from label, time, and a key when the publisher sent one. Ids are 64-bit, server-assigned, and only increase; they never repeat, not across restarts, not after pruning. The server keeps at most 100000 events and nothing older than 7 days. Pruning runs on the write path, amortised over inserts, with index-driven deletes. Constants, not flags.

- Topic: 1–255 bytes, segments separated by `/`, each segment `[A-Za-z0-9_.-]+`. No empty segments, so no leading or trailing `/` and none doubled. `#`, `+`, and whitespace never appear in a published topic.
- Body: any bytes, including newlines and binary, at most 1 MiB (1048576). Empty is allowed. 1 MiB is a protection cap, not a format rule.
- Key (optional): 1–128 bytes of printable ASCII without spaces. When the same `from` publishes the same key while the original is still retained, the original event comes back — same id, `duplicate` set — and nothing is stored. One transaction behind a unique index makes this race-free. A key expires with its event.

| Door | `from` |
| --- | --- |
| `http://event.<domain>/api/events` — a device that speaks HTTP. Access token required | the token's comment, when it is a valid label; else `token-<id>` |
| `event pub` / `event get` — the control REPL | `ssh` |
| `box event` on the server, over the localhost socket | `server` |
| `box event` on a computer — agent socket, then the control stream | the computer's name |

A client never names its own `from`; a `from` in a JSON body is ignored. Give each device its own token with a meaningful comment: `box token add door-sensor`.

### Reading

`GET /api/events?since=&topic=&from=&limit=&wait=`. `since` is an id cursor, default 0. `topic` and `from` are repeatable; each list is a union. A topic filter is an exact topic, or a prefix ending in `/#`: `site1/#` matches `site1/a` and `site1/a/b`, not `site1` itself and not `site10/x`. A bare `#` matches everything. Prefix filters walk an index range, never `LIKE`. In a URL, `#` is written `%23`: `topic=site1/%23`. `limit` is 1–1000, default 100. Results are ordered by id ascending, ids strictly greater than `since`, at most `limit`.

Every read response carries the window, so a client can detect gaps and start from now:

```json
{"events":[...],"oldest":123,"latest":456,"more":false}
```

`oldest` is the smallest retained id, `latest` the largest id ever assigned; both are 0 on an empty log, and `latest` survives pruning and restarts. A client whose `since` is below `oldest-1` missed events. `more` is set when `limit` cut the result.

With `wait` (0–25 whole seconds, default 0) and no matching event after `since`, the read blocks until a matching publish or the wait ends, then answers — possibly empty, always with the window. A publish broadcasts in-process; each wake-up re-queries SQLite; no goroutine polls the database. The wait ends early when the client leaves, the control stream closes, or the server stops. 25 seconds stays under the agent's 30-second call timeout to the server. Concurrent waiters are not capped; each holds one goroutine and one open request.

A body that is valid UTF-8 is a JSON string in `body`; any other bytes are base64 in `body_b64`. An empty body carries neither field.

### Publishing

- `POST /api/events` with JSON `{"topic","body","key"?}`. Binary bodies use `"body_b64"` instead of `"body"`; sending both is 400. A `from` field is ignored. Answers `201 {"id":n}`; a dedup hit answers `200 {"id":n,"duplicate":true}`.
- `POST /api/events/<topic>` with any body and any content type: the request bytes are stored verbatim. This is the one-line path for tiny devices. The key rides `Idempotency-Key` or `?key=`. Same answers.
- A body over 1 MiB is 413. A bad topic, key, or filter value is 400 with a one-line reason.

`event.<domain>` answers only `/api/events` and `/api/events/...` and never redirects to sign-in. Computers use `box event`, not the HTTP API.

### Small devices

Ids are 64-bit. Store the cursor in a 64-bit integer; a 32-bit counter (some ArduinoJson configurations) wraps. Two patterns with curl:

```bash
curl -H "X-Box-Token: $TOKEN" -H "Idempotency-Key: door-1" \
  -d 'open' http://event.box.example.com/api/events/kitchen/door

while true; do
  curl -s -H "X-Box-Token: $TOKEN" \
    "http://event.box.example.com/api/events?since=$SINCE&topic=kitchen/%23&wait=25"
  # advance SINCE to the last id seen, or to latest on the first answer
done
```

On a computer, `box event get --follow` runs that loop and prints one line per event; `box event get --since 0` replays what is retained.

## Access tokens

Only the server creates them. `token add [comment]` prints a token with no expiry; `--for 12h` sets a lifetime (`30d` and Go durations such as `720h` work). `token ls` prints tokens again, and the TUI tokens screen keeps them on screen, so the operator can copy them any time. `token rm <id>` revokes one. A revoked or expired token stops opening private portals and the event API, including through a cookie the browser already holds. Never log a token.

## Control REPL and TUI

The SSH command is the control plane. Non-interactive sessions (`ssh box.example.com ls`) print plain text, or JSON with `--json`. Interactive sessions get a bubbletea TUI. Both share one service layer.

| Command | Effect |
| --- | --- |
| `ls` | Computers: name, online, user, address, agent version, portals |
| `ssh <name>` | Open a shell on that computer |
| `rm <name>` | Delete the computer and revoke its token. Asks for the name again |
| `rename <name> <new>` | Rename. Portals stay claimed; the SSH username changes |
| `stat <name>` | Live load from the agent: cpu, memory, disk, uptime |
| `pending` | Computers waiting for approval |
| `approve <code>` | Approve a pending join |
| `pair` | Print a one-time password for another client |
| `key ls` / `key rm` | List or remove bound client keys |
| `env set <name> <value>` | Store a variable. New splice sessions receive it |
| `env rm <name>` | Remove it. Existing sessions keep the old value |
| `env ls` | List names only |
| `token add [--for 12h] [comment]` | Create an access token |
| `token ls` / `token rm <id>` | List tokens with their secret, or revoke one |
| `event pub <topic> [text…] [--key k]` | Append one event. Text args join into the body. `from` is `ssh` |
| `event get [--since n] [--topic t]… [--from f]… [--limit n] [--wait s]` | Print events newer than an id, oldest first. `--topic` takes `prefix/#` too |
| `whoami` | Which key this session used |

The TUI is always dark. Screens, keyed 1–7: **summary**, computers, pending (with the approval form), portals, keys, env names, tokens. It opens on the summary:

- Fleet tiles: computers online, pending joins, open and total spliced SSH sessions, control consoles, portal requests (rate, denied, failed), tunnel traffic and average RTT, server uptime, portal and token counts, tokens expired or expiring within a week.
- One card per computer: RTT gauge, load sparkline, memory and disk used, traffic rate, open and total `ssh` and `portal` streams, tunnel age, host uptime, packet loss. Offline computers show as offline.
- The newest events.

Counters start when `box serve` or the tunnel starts; sparklines keep about 80 seconds of samples in the TUI and reset when it reopens. Load is asked from each live agent in parallel with a one-second timeout, and only while the summary is showing. The summary holds no secrets.

The layout works down to 30×10. As width shrinks, tabs shorten to number keys, table columns drop by priority, fleet tiles fold into one overview card, and cards stack. The selected token and the one-time password wrap rather than being cut. The status line shows a notice for five seconds — an action's result, or a computer joining, coming online, or going offline — then falls back to the key hints.

On the server machine, `box` with no arguments opens the same TUI over the localhost socket. It can approve, remove computers, remove keys, and add and remove tokens. It cannot bind a client key; that stays on the SSH password path. `box token` and `box event` on the server use the same socket. On a computer, `box event` uses the agent socket and `box token` is refused.

## Processes

| Machine | Process | Listens |
| --- | --- | --- |
| Server | `box serve` | SSH, HTTP, QUIC, and a localhost socket |
| Computer | `box join`, then `box agent` | nothing public; a unix socket in `~/.box` |

`box agent` reads `~/.box/computer.json` and reconnects. It is meant to run under a systemd user unit.

## Data

One SQLite file in the data directory (default `/var/lib/box`, `--data-dir`). It is the secret store and is not world-readable. The file runs in WAL journal mode with `synchronous=NORMAL`.

- `keys`: public key, comment, bound at
- `pairings`: hash of a one-time password, expiry, used at, failed attempts
- `computers`: name, token hash, login user, sshd host public key, joined at
- `portals`: hostname, computer, port, private (default false), claimed at
- `tokens`: access token, comment, expiry, created at. The value is stored so it can be copied again
- `env`: name, value. List commands never return the value
- `events`: id (AUTOINCREMENT, int64), topic, body (any bytes), from label, token id, dedup key, created at. Indexed on (topic, id) and (from, id); a partial unique index on (from, dedup key) makes dedup race-free. At most 100000 rows, none older than 7 days
- `meta`: domain, the three listen addresses, server keys, schema version

Pending joins and TUI counters live in memory only. The agent keeps `~/.box/computer.json`: token, QUIC endpoint, server fingerprint, name, login user.

## Install and deploy

One release archive per OS and architecture holds the `box` binary. `scripts/install.sh` downloads and verifies it, then asks: server or computer.

- **Server**: install, optionally a systemd unit, run `box serve --domain box.example.com`, open the three ports. The first start prints the one-time password.
- **Computer**: install, run `box join <domain>`, approve the code at the server, optionally install a systemd user unit for `box agent`.

## Not in the first version

- accounts, billing, invites, teams, SSO, email
- a web coding agent
- per-computer key scoping
- a TCP fallback for the tunnel; networks that block UDP cannot run a computer
- TLS on the HTTP port
- moving a computer's identity between machines
- resource limits on a computer
- CDN

## Dependencies

| Piece | Choice | Why |
| --- | --- | --- |
| Client | OpenSSH, already installed | No client to ship |
| Control SSH | charmbracelet/wish | An sshd we can branch on username and auth method |
| TUI | bubbletea + lipgloss | wish's companion; one framework for REPL and dashboard |
| Tunnel | quic-go | Independent streams, one TLS stack, NAT rebinding, connection stats |
| Server state | SQLite | One file, enough for a private deployment |
| Computer SSH | the machine's openssh-server | A normal sshd, so scp and Remote-SSH work |
