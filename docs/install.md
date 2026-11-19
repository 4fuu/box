# Install and deploy

One release archive per OS and architecture holds the `box` binary. Releases use a calendar version such as `2026.924.0`; see [release](release.md).

## Requirements

- a Linux machine for the server, and a Linux or Windows machine for each computer
- OpenSSH on the machine you connect from, and sshd on each computer. On Windows, the OpenSSH Server optional capability provides sshd; it can be installed after `box join`, and the computer shows ssh disabled until `box sshd enable` succeeds. The agent socket needs Windows 10 1809 or newer

The server and a computer can be the same machine. The agent does not need root.

## Linux: the installer

Download the installer and run it in a terminal. It asks for English or Chinese, then whether this machine is the server or a computer.

```bash
curl -fsSL -o install.sh https://raw.githubusercontent.com/4fuu/box/main/scripts/install.sh
sh install.sh
```

The script downloads and verifies the release archive and installs the `box` binary. It does not install a container runtime or frp.

### Server

A server install can add a systemd unit. Open three ports: SSH (default `:22`), HTTP (default `:80`), and QUIC (default `:7443`, UDP). Then:

```bash
box serve --domain box.example.com
```

The first start prints a one-time password, valid for five minutes; the first SSH client to present it binds its key. Later starts reuse the stored domain and ports. See [control](control.md) for binding more clients.

### Computer

A computer install runs `box join <domain>` and can add a systemd user unit for `box agent`. Approve the printed code at the server: enter it in the TUI (pending screen), or run `approve <code>` from a bound client.

```bash
box join box.example.com
box agent
```

`ssh home@box.example.com` is that computer's sshd. The SSH username is the name registered on the server; the shell runs as the account that ran `box join` on that machine, unless `--user` names another account. The two names do not have to match. [Computers](computers.md) describes what join writes and how the agent behaves.

## Windows computer

There is no installer. Download the `windows-amd64` zip from the [releases](https://github.com/4fuu/box/releases), put `box.exe` on PATH, and run `box join <domain>`. Join succeeds even before sshd exists, but the computer's ssh stays disabled. In an elevated PowerShell install the OpenSSH Server capability:

```powershell
Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0
Set-Service sshd -StartupType Automatic
Start-Service sshd
```

Then run `box sshd enable` on the computer. It checks the host key and that sshd answers, then reconnects the agent so the server learns the key. Keep `box agent` running at logon from the Startup folder (`shell:startup`) or a Task Scheduler job that runs `box.exe agent` when you log on.

Windows sshd reads keys for administrator accounts from `C:\ProgramData\ssh\administrators_authorized_keys`, not `~/.ssh`; box prints that note at join time. Editing the config and restarting the service need an elevated PowerShell. See [computers](computers.md) for the full sshd behavior.
