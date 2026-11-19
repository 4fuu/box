# Control: pairing, the REPL, the TUI

The SSH command is the control plane. Non-interactive sessions (`ssh box.example.com ls`) print plain text, or JSON with `--json`. Interactive sessions get a bubbletea TUI. Both share one service layer. `box help` on any machine prints the full command syntax.

## Binding clients

There is no account. Trust is a public key the server accepted.

At init the server prints a one-time password valid for five minutes. While it is live, the first client to connect with any key gets a password prompt inside the session; typing it binds that client's key, after which the password is refused. The password is never written to the client.

To bind another client, run `pair` from a bound client, or `box pair` on the server machine. Keys are listed and removed from a bound client or the localhost CLI. Removing the last key does not unlock the server; initialize again or pair from localhost. Every bound key reaches every computer.

## REPL commands

| Command | Effect |
| --- | --- |
| `ls` | Computers: name, online, ssh, user, address, agent version, portals |
| `ssh <name>` | Open a shell on that computer |
| `rm <name>` | Delete the computer and revoke its token. Asks for the name again |
| `rename <name> <new>` | Rename. Portals stay claimed; the SSH username changes |
| `stat <name>` | Live load from the agent: cpu, memory, disk, uptime. Windows reports memory, disk, and uptime from the system APIs; there is no load average, so cpu stays unset |
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

The `ssh` column in `ls` reads `disabled` for a computer without an sshd host key; see [computers](computers.md#enabling-ssh).

## The TUI

The TUI is always dark. Screens, keyed 1–7: **summary**, computers, pending (with the approval form), portals, keys, env names, tokens. It opens on the summary:

- Fleet tiles: computers online, pending joins, open and total spliced SSH sessions, control consoles, portal requests (rate, denied, failed), tunnel traffic and average RTT, server uptime, portal and token counts, tokens expired or expiring within a week.
- One card per computer: RTT gauge, load sparkline, memory and disk used, traffic rate, open and total `ssh` and `portal` streams, tunnel age, host uptime, packet loss. Offline computers show as offline. A computer whose ssh is disabled is marked on its card.
- The newest events.

Counters start when `box serve` or the tunnel starts; sparklines keep about 80 seconds of samples in the TUI and reset when it reopens. Load is asked from each live agent in parallel with a one-second timeout, and only while the summary is showing. The summary holds no secrets.

The layout works down to 30×10. As width shrinks, tabs shorten to number keys, table columns drop by priority, fleet tiles fold into one overview card, and cards stack. The selected token and the one-time password wrap rather than being cut. The status line shows a notice for five seconds — an action's result, or a computer joining, coming online, or going offline — then falls back to the key hints.

## The localhost CLI

On the server machine, `box` with no arguments opens the same TUI over the localhost socket. It can approve, remove computers, remove keys, and add and remove tokens. It cannot bind a client key; that stays on the SSH password path. `box token` and `box event` on the server use the same socket. On a computer, `box event` uses the agent socket and `box token` is refused.
