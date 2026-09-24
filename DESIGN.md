# Design

A self-hosted persistent Linux computer. The client is the `ssh` already installed on the operator's machine. The server is the only public SSH entry. A deploy node is a controller on a machine the operator owns. It may sit behind NAT. microsandbox is one runtime inside that controller, and can be replaced later.

The reference is [exe.dev](https://exe.dev): one command yields a computer, the disk survives restarts, and a website on that computer gets a hostname. Billing, accounts, email, the web agent, and per-VM public IPs are out of scope. This is a private deployment, not a hosted service.

## Status

Design only. Nothing here is implemented.

## Binding

There is no account. Trust is a public key, or a node token, accepted by the server.

When the server is initialized it prints a one-time password and the time it expires. The first SSH connection that presents the password is bound: the server stores that client's public key and then refuses the password. Expiry also refuses it. The password is never written to the client.

To bind another client, either:

- from an already bound client, run `pair`, which prints a new one-time password, or
- on the server machine, run `box server pair`, which listens on localhost only and prints one.

A deploy node joins with a pairing code, not a password. The code only confirms that this machine is the one the operator intends to add. It is short, single use, and expires. `node pair` on a bound client prints one. `box server node-pair` on the server localhost does the same. The node stores the resulting token locally and reconnects with it. The code is not used again.

A bound key can be listed and removed from a bound client, or from the localhost CLI. Removing the last key does not unlock the server. Initialize again, or pair from localhost.

## Use

```
ssh box.example.com
```

On an unbound server this asks for the one-time password, then opens the control REPL. Later connections with the same key open the REPL directly.

```
box ▶ new
  name: web
  image: base (default)
  node: home (default)
  cpu: 2
  memory: 2G
  disk: 20G
web ready
http://web.box.example.com
ssh vm+web@box.example.com
```

`http://` is the server's fixed routing port. TLS, if any, is terminated by an edge the operator runs in front of that port. This project does not issue certificates.

```
box ▶ ssh web
```

From outside the REPL, the same computer is:

```
ssh vm+web@box.example.com
```

`scp`, `rsync`, and VS Code Remote-SSH use that destination. `vm+` is routing information, not the user inside the computer. The base image logs in as `box`, with passwordless sudo.

A node that has just started cannot create a computer. Pull the image first:

```
box ▶ image pull base
```

## Three parts

```diagram
client                      server                         deploy node
OpenSSH                     public SSH entry               operator's machine

ssh box.example.com         control REPL
ssh vm+name@box             key check, then forward
                            fixed HTTP port, route by Host
                                     │
                                     │ frp, node dials out
                                     ▼
                            commands ──────────────▶  controller
                            SSH channels ──────────▶     │
                            HTTP by Host  ──────────▶    ▼
                                                      microsandbox
                                                      computer from base image
```

The client is not a program we ship.

The server checks the key, serves the control REPL, forwards an authorized SSH channel to the node that owns the computer, and routes HTTP by the `Host` header. It does not run the user's processes.

The controller receives commands and connections from the server. It pulls images, creates and stops computers, and attaches a connection to the matching computer. Clients cannot dial the controller. The controller does not store client public keys.

The runtime in the first version is microsandbox. The controller calls it through a small set of actions: start from a spec, keep a disk, provide a shell and SFTP, publish a loopback port, report status. Replacing it with Cloud Hypervisor or Firecracker changes only that layer.

## Server to node

[frp](https://github.com/fatedier/frp) carries every byte between the server and a node. The node dials out, so it can sit behind NAT. frp encrypts that path. The operator's public edge is a separate concern and is not part of this path.

The node registers three proxies after pairing:

| Name | On the node | Use |
| --- | --- | --- |
| `node-<id>-rpc` | controller RPC, loopback only | create, start, stop, status, resize |
| `node-<id>-attach` | per-computer SSH attach, loopback only | shell, exec, SFTP, local forward |
| `node-<id>-http` | HTTP for computers on this node | public edge routing |

RPC and attach use STCP. They are not published on a public port. The server opens a short-lived visitor when it needs one, connects to loopback, and closes it.

HTTP uses frp's HTTP virtual-host proxy. The server listens on one fixed port. A request is routed by `Host`. The node registers a custom domain only after the server has accepted it. The fixed port is for the operator's edge. It is plain HTTP. Do not publish it to the internet without that edge.

xtcp is not used. NAT traversal is unreliable, and STCP through frps is enough.

The client's SSH connection is not spliced through to the node. An SSH handshake cannot be handed off mid-flight, and doing so would make the node validate client keys. The server finishes authentication, then opens its own SSH connection over STCP to the node's attach port. The client does not see this hop and does not configure `ProxyJump`.

## SSH entry

The server speaks SSH with [charmbracelet/wish](https://github.com/charmbracelet/wish). There is no default shell. An unknown key is accepted only while presenting a live one-time password. After that, only bound keys are accepted.

SSH has no Host header. One port cannot be demultiplexed by the name the client typed. Both destinations therefore share the server address, and the username carries the route.

| Username | Result |
| --- | --- |
| empty, `box`, or anything not starting with `vm+` | control REPL |
| `vm+web` | the computer named web |
| `pair+<password>` | bind this key, if the password is valid |

`pair+` exists so a password can be passed non-interactively. The interactive REPL asks for it when a key is not yet bound.

Inside a computer session, Wish allocates no shell of its own. It forwards the session, exec, SFTP, and `direct-tcpip` (`ssh -L`). Reverse forward (`-R`) is rejected. microsandbox does not support it either.

An SSH disconnect does not stop the computer. The controller disables microsandbox's default 10-minute SSH idle timeout when it creates the computer.

## Control REPL

The control plane is the SSH command itself, and a REPL for a person. Output is for a person. `--json` is for scripts. Names follow exe.dev where the action is the same.

Computers:

| Command | Effect |
| --- | --- |
| `new [name]` | Create. With no flags, ask for image, node, cpu, memory, disk |
| `ls` | Name, state, node, image, HTTP host |
| `ssh <name>` | Open a shell on that computer |
| `rm <name>` | Delete the computer and its disk. Ask for the name again |
| `restart <name>` | Restart |
| `rename <name> <new>` | Rename. The HTTP host follows |
| `resize <name>` | Change cpu, memory, or disk. Disk only grows |
| `stat <name>` | cpu, memory, disk, network |

Nodes and images:

| Command | Effect |
| --- | --- |
| `node ls` | Online nodes, capacity, used, tags, images present |
| `node pair` | Print a one-time pairing code |
| `node rm <name>` | Remove a node. Refused while it still has computers |
| `node tag <name> k=v` | Tag a node so `new` can select it |
| `image ls` | Registered images, and which nodes have pulled them |
| `image add <name> <ref>` | Register a registry reference |
| `image pull <name> [node]` | Pull on one node, or all online nodes |
| `image rm <name>` | Drop the registration and the node cache. Refused while in use |
| `image default <name>` | Image used when `new` does not name one |

HTTP hosts:

| Command | Effect |
| --- | --- |
| `share port <name> <port>` | Port on the computer that the HTTP host routes to. Default 8000 |
| `domain add <name> <fqdn>` | Accept this Host and route it to the computer |
| `domain rm <name> <fqdn>` | Remove it |

Keys:

| Command | Effect |
| --- | --- |
| `pair` | Print a one-time password for another client |
| `key ls` / `key rm` | List or remove bound client keys |
| `whoami` | Which key this session used |
| `defaults` | Default node, image, and size. Non-interactive `new` uses these |

Non-interactive create:

```
ssh box.example.com new web --image base --node home --cpu 2 --memory 2G --disk 20G --json
```

If the chosen node has not pulled the image, `new` tells the operator to run `image pull`. It does not pull implicitly. A pull can take long enough that `new` would look stuck.

The localhost CLI on the server is `box server`. It can `pair`, `node-pair`, `key ls`, `key rm`, and `status`. It does not open a path around the REPL for creating computers. That stays on a bound client, so a person on the server console cannot skip the key check by accident. `status` is the exception: it is read-only.

## HTTP routing

The server exposes one fixed HTTP port. Routing is the `Host` header, through frp's HTTP proxy. A host is routed only after `domain add` has accepted it. Anything else gets 421. That stops a computer, or an outside client, from claiming a name that points at the server.

The default host for a new computer is `<name>.<base-domain>`, registered at create time. Extra names are `domain add`.

There is no login wall and no certificate issuance here. The operator's edge terminates TLS, applies whatever access policy they want, and forwards HTTP to this port. Because the port is plain HTTP, the edge should be the only client allowed to reach it.

Port 5432 and other non-HTTP ports are not published. SSH does not go through this port. Publishing a range of HTTP ports on one host can be added later. The first version routes one port per host.

## Guest CLI

Each computer has a `box` command on its `PATH`. It talks only to the controller on that node, over a socket the controller publishes into the computer. It cannot reach the server, and it cannot see other computers.

| Command | Effect |
| --- | --- |
| `box http status` | Hosts and the port they route to |
| `box http port <port>` | Ask the controller to change the routed port |
| `box domain add <fqdn>` | Ask the controller to register a host |
| `box domain rm <fqdn>` | Ask the controller to remove it |

The controller checks the request, then asks the server. The server accepts or refuses. Only then does the controller register or drop the frp HTTP proxy. A compromised computer must not be able to write frp config directly. That would let it take another computer's host.

A skill ships in the base image at `/home/box/.agents/skills/box/SKILL.md`. It tells an agent on the computer to use `box http` and `box domain` rather than editing a proxy config or opening a port on the node. The skill does not contain credentials.

## Images

The base image is a Debian computer, not a language runtime. The Dockerfile is `images/base/Dockerfile`. It starts from `ghcr.io/superradcompany/debian-systemd:12`, so the controller can hand PID 1 to systemd with `--init auto`.

The image contains:

- systemd, sudo, an SSH client, and CA certificates
- user `box`, uid 1000, passwordless sudo, home `/home/box`
- git, curl, jq, vim, python3, build-essential, ripgrep
- an empty `/etc/machine-id`, so each computer generates its own on first boot
- an `/etc/fstab` entry that grows the root filesystem when the disk grows
- the label `box.login-user=box`, which the controller uses as the SSH user
- `EXPOSE 8000`, as documentation only. The routed port lives in the server's image record

The image has no secrets and no per-computer host key. SSH is terminated by microsandbox on the node. sshd inside the computer stays disabled.

Build and publish:

```
docker build -t base:local images/base
crane push base:local registry.example.com/base:latest
```

The registry is the operator's. The server only stores the reference. `image pull` runs `msb pull` on the node. A node that has never pulled `base` cannot create a computer from it. That is deliberate: a node is not usable until it has pulled an image.

## Controller

One binary, `box-node`.

1. Pair with the server. Store the node token.
2. Start frpc and register the RPC, attach, and HTTP proxies.
3. Serve loopback RPC.
4. Call microsandbox for each command.
5. Report cpu, memory, free disk, and pulled images on heartbeat.

Join:

```
box-node join --server box.example.com:7000 --code <pairing-code> --name home
```

The code is single use. The node id and token are written to `/var/lib/box-node/node.json`. A restart reconnects with that file.

Creating a computer:

1. Refuse if the image is not in the local cache.
2. `msb create` with the registered ref, `--init auto`, the requested cpu and memory, and `--deployment-profile multi-tenant`.
3. Put the disk on a microsandbox named volume mounted at `/`. Do not use the OCI writable layer as the computer disk. `msb rm` deletes that layer. A named volume survives it.
4. Disable the SSH idle timeout. Authorize only the server's attach key. That key represents the server, not a client.
5. Run `msb ssh serve --host 127.0.0.1 --port 0` for this computer and record the port.
6. Publish the computer's HTTP port on loopback, and register that upstream with frp under the accepted hosts.

When the server forwards a session, the controller connects to that loopback port as `box.login-user`. The client key was already checked on the server.

If the controller restarts, it asks microsandbox which computers still exist, rebinds their serve ports, and reports status. The disk is the named volume, not the controller process.

If the node lacks capacity, the RPC returns a specific error. The server shows it and names another node if one has the image. The controller does not place computers itself.

## Placement

`new` without `--node` uses the default node. If there is no default and only one node is online, that node is used. If more than one is online, `new` asks.

A computer on an offline node is shown as `offline`. It is not moved. Moving a disk is out of scope. The operator can `new` a different computer on another node.

The server will not place a computer on a node that has not pulled the image.

## Data

The server keeps one SQLite file.

- `keys`: public key, comment, bound at
- `pairings`: hash of a one-time password or node code, expiry, used at
- `nodes`: name, tags, last heartbeat, capacity
- `images`: name, ref, default port, whether it is the default
- `computers`: name, node, image, size, state, HTTP port
- `domains`: computer, host

Computer names are globally unique, because the HTTP host is `<name>.<base-domain>`. `new` refuses a collision.

The controller keeps its own local record: node token, microsandbox name, attach port, volume name. The server does not store those.

## Processes

On the server, one process supervisor runs:

| Process | Listens | Role |
| --- | --- | --- |
| `boxd` | 22, and the fixed HTTP port | SSH, REPL, key check, Host routing |
| `frps` | 7000, token required | Meeting point for nodes |

`frps` is not for clients. 7000 requires the token issued at pairing.

On a node, `box-node` is the only process the operator starts. It starts frpc and microsandbox. The node needs KVM, with `/dev/kvm` readable by the user running `box-node`. It does not need root.

## Not in the first version

- accounts, billing, invites, teams, SSO
- email
- a web coding agent
- secret injection at the edge
- a public IP per computer, or an SSH hostname per computer
- TLS and public access control on the HTTP port
- moving a computer between nodes
- reverse port forward
- CDN

## Dependencies

| Piece | Choice | Why |
| --- | --- | --- |
| Client | OpenSSH, already installed | No client to ship |
| SSH server | charmbracelet/wish | An sshd we can branch on the username |
| Server to node | frp | NAT traversal, encryption, and HTTP host routing |
| Computer | microsandbox | The runtime the operator asked for. Replaceable |
| Base image | debian-systemd:12 | A systemd guest maintained for that runtime |
| Server state | SQLite | One file, enough for a private deployment |

The REPL, the controller RPC, the guest `box` CLI, and the skill are the code to write. The rest is the software above.
