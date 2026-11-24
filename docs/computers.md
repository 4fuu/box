# Computers

A computer is a whole machine that joins the server and keeps an agent running. The agent runs as a regular user and never needs root.

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

## The agent

The login user is whoever ran `box join`, unless `--user <name>` names another existing account. `ssh home@box.example.com` opens a shell as that account on the computer registered as `home`; the two names need not match.

The agent manages one file, `~/.ssh/box_authorized_keys`, kept in sync with the server's bound keys. The user's own `authorized_keys` is untouched. `box join` adds the file to sshd's `AuthorizedKeysFile` when it can write the config, prints each change, and reloads sshd when it can; otherwise it prints the lines and the reload command. On Windows the config is `C:\ProgramData\ssh\sshd_config` (OpenSSH Server is an optional capability; when it is missing `box join` prints the install command and still succeeds, and the computer shows ssh disabled), editing the config and restarting the service need an elevated PowerShell — there is no reload — and the default `Match Group administrators` block sends administrator accounts to `administrators_authorized_keys` instead of `~/.ssh`; box prints that note whenever the block is present.

### Two-node failover

Two servers can share one domain; see [architecture](architecture.md#two-nodes). The computer joins at the configuration writer and receives both QUIC endpoints in the join reply. The agent races the pinned handshakes at startup and on every reconnect, tries the login one node at a time, and stays with the first node that accepts it until the tunnel closes. Every hello refreshes the list, so a changed endpoint reaches the computer without editing `computer.json`.

The writer replicates configuration to the replica. A computer attached to the replica can still claim portals: the replica forwards the claim to the writer and the new row comes back in the next snapshot. If the writer is down, existing portals keep serving at the replica but configuration changes fail.

A manual `nodes` array in `~/.box/computer.json` still works as a recovery path, with the same shape:

```json
"nodes": [
  {"quic": "node-a.example.com:7443", "fingerprint": "<node-a certificate fingerprint>"},
  {"quic": "node-b.example.com:7443", "fingerprint": "<node-b certificate fingerprint>"}
]
```

A refusal in multi-node mode is retried rather than treated as authorization to wipe the agent's state; the SSH bootstrap is the fallback that reports a revoked token. Removing `nodes` restores the single-server path. A silent network failure can take about 30 seconds for QUIC to detect before the agent starts reconnecting.

## Enabling ssh

Until sshd is installed, configured, and answering, the server shows that computer's ssh as disabled and a splice fails with that state. `box sshd enable` runs the same config edit, then checks that the sshd host public key reads and parses as an authorized key and that sshd answers on its local address with an SSH identification line. Only when both pass does it close the current tunnel so the next hello carries the host key; a failed probe leaves ssh disabled. When the tunnel is already down, a passing probe still stands and the key reaches the server at the next reconnect.

The disabled state shows in the REPL `ls` and the TUI: an SSH column in the computers table, and a marker on the summary's machine cards. A splice to a disabled computer says to run `box sshd enable` on it.

## Env

`env set` values live on the server only. Each splice session receives the current set as SSH env requests, so values exist on the computer only in that session's memory. sshd drops names outside `AcceptEnv`, so `box join` adds `AcceptEnv *` when no global `AcceptEnv` line exists and says so; existing lines are left alone. `env ls` prints names, never values.

## The box skill

The agent writes the box skill to `~/.agents/skills/box/SKILL.md`. It holds no credentials. It is a short usage guide for a coding agent on this machine; `box help` carries the full syntax. The skill source is [internal/agent/skill/SKILL.md](../internal/agent/skill/SKILL.md), and it can also be added to any agent with `npx skills add 4fuu/box --skill box`.

## The guest CLI

On a computer, `box` is the guest CLI on the agent socket `~/.box/agent.sock` (an AF_UNIX socket; Windows 10 1809 or newer):

```
box domain
box.example.com
second.example
box portal check web --domain second.example
free
box portal add web 3000 --domain second.example
http://web.second.example
box portal add lock 3000 private --domain box.example.com
box event pub door open
box event get --since 0
box event get --follow --topic door
box sshd enable
box help
```

`box domain` prints every parent domain the server serves, one per line. When it serves one, `--domain` is optional; when it serves several, `check`, `add`, and `rm` require it. `box help` prints the guest help, and bare `box` prints it too. See [portals](portals.md) and [events](events.md) for what the commands do.
