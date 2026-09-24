# Design

A self-hosted persistent Linux computer. The client is the `ssh` already installed on the operator's machine. The server is the only public SSH entry. A deploy node is a controller on a machine the operator owns. It may sit behind NAT. The controller runs each computer as a Podman container. One node runs many containers. There is no microVM and no second SSH hop.

The reference is [exe.dev](https://exe.dev): one command yields a computer, the disk survives restarts, and a website on that computer gets a hostname. This is a private deployment, not a hosted service. A container shares the node's kernel. That is accepted. A private node does not need a hardware VM boundary.

## Status

The `box` module implements this spec. This document is still the source of truth when it disagrees with the README.

## Binding

There is no account. Trust is a public key, or a node token, accepted by the server.

When the server is initialized it prints a one-time password and the time it expires. The first SSH connection that presents the password is bound: the server stores that client's public key and then refuses the password. Expiry also refuses it. The password is never written to the client.

To bind another client, either:

- from an already bound client, run `pair`, which prints a new one-time password, or
- on the server machine, run `box pair`, which listens on localhost only and prints one.

A deploy node joins with a pairing code, not a password. The code only confirms that this machine is the one the operator intends to add. It is short, single use, and expires. `node pair` on a bound client prints one. `box node pair` on the server localhost does the same. The node stores the resulting token locally and reconnects with it. The code is not used again.

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
ssh web@box.example.com
```

`http://` is the server's fixed routing port. TLS, if any, is terminated by an edge the operator runs in front of that port. This project does not issue certificates.

```
box ▶ ssh web
```

From outside the REPL, the same computer is:

```
ssh web@box.example.com
```

The username is the container's name. The server looks up which node runs that container and splices the TCP connection to that container's sshd. The session is inside the container, not on the controller. `scp`, `rsync`, and VS Code Remote-SSH use that destination. The login user inside the container is `box`, from the image. The username does not select that user. It selects the container.

A node that has just started cannot create a computer. Pull the image first:

```
box ▶ image pull base
```

## Three parts

```diagram
client                      server                         deploy node
OpenSSH                     public SSH entry               operator's machine

ssh box.example.com         control REPL
ssh web@box                 username is the container; splice to its sshd
                            fixed HTTP port, route by Host
                                     │
                                     │ frp, node dials out
                                     ▼
                            commands ──────────────▶  controller
                            SSH TCP    ──────────────▶  that container's sshd
                            HTTP by Host ───────────▶  that container's bridge IP:port
```

The client is not a program we ship.

The server checks the key, serves the control REPL, and splices an authorized SSH TCP connection to the container named by the username. The handshake finishes at sshd inside that container. It does not finish at the controller, and it does not open a shell on the node. The server also routes HTTP by the `Host` header. It does not run the user's processes.

The controller receives commands from the server. It pulls images, creates and stops containers, and publishes each container's sshd on loopback. HTTP is not published as a host port. The controller forwards to the container's bridge address and the port the container asked for. Clients cannot dial the controller. The controller does not store client public keys. It does store the bound public keys for the containers it runs, pushed by the server, so the container's sshd can accept the spliced connection.

Podman is the runtime, not a plugin behind another runtime. The controller shells out to `podman`. Replacing it later means replacing those calls. The first version does not keep a second backend.

## Server to node

[frp](https://github.com/fatedier/frp) carries every byte between the server and a node. The node dials out, so it can sit behind NAT. frp encrypts that path. The operator's public edge is a separate concern and is not part of this path.

The node registers three proxies after pairing:

| Name | On the node | Use |
| --- | --- | --- |
| `node-<id>-rpc` | controller RPC, loopback only | create, start, stop, status, resize, sync keys |
| `node-<id>-ssh` | one TCP proxy per computer, loopback only | the container's sshd |
| `node-<id>-http` | HTTP for portals on this node | one upstream per claimed host |

RPC and SSH use STCP. They are not published on a public port. The server opens a short-lived visitor when it needs one, connects to loopback, and closes it. The SSH visitor is a TCP splice. The client's SSH handshake ends at sshd in the container the username named. The controller only published that container's port. There is no second SSH session and no `ProxyJump`.

HTTP uses frp's HTTP virtual-host proxy. The server listens on one fixed port. A request is routed by `Host`. The node registers a custom domain only after the server has accepted it. The fixed port is for the operator's edge. It is plain HTTP. Do not publish it to the internet without that edge.

xtcp is not used. NAT traversal is unreliable, and STCP through frps is enough.

## SSH entry

The server speaks SSH with [charmbracelet/wish](https://github.com/charmbracelet/wish) for the control REPL only. An unknown key is accepted only while presenting a live one-time password. After that, only bound keys are accepted.

SSH has no Host header. One port cannot be demultiplexed by the name the client typed. The username is the route, and it names a container. The server resolves that name to the node that runs it, then splices. A node runs many containers. Each has its own sshd, its own loopback port, and its own disk. The username never selects the node as a machine to log into.

| Username | Result |
| --- | --- |
| empty, or `box` | control REPL |
| `web` | TCP splice to the container named web, if this key may access it |
| `pair+<password>` | bind this key, if the password is valid |

A container name cannot be `box` or `pair`, and cannot contain `+` or `.`. `new` refuses those. Names are globally unique, so the username alone is enough. `pair+` exists so a password can be passed non-interactively. The interactive REPL asks for it when a key is not yet bound.

The server accepts the key before it splices, so an unknown key never reaches a container. The container's sshd must also accept that key, because the splice is a new TCP connection and the handshake runs again, this time against the container. The server pushes the bound public keys to the node. The controller writes them to the computer's authorized keys. A key removed on the server is removed on the next push. Sharing is a key on that list, not a second account system.

`scp`, `rsync`, SFTP, and `ssh -L` work because they arrive at a normal sshd. Reverse forward (`-R`) is allowed by sshd and is not specially disabled. It can only reach addresses the container can route. It is not a feature of the control plane.

An SSH disconnect does not stop the container.

## Control REPL

The control plane is the SSH command itself, and a REPL for a person. Output is for a person. `--json` is for scripts. Names follow exe.dev where the action is the same.

Computers:

| Command | Effect |
| --- | --- |
| `new [name]` | Create. With no flags, ask for image, node, cpu, memory, disk |
| `ls` | Name, state, node, image, portals |
| `ssh <name>` | Open a shell on that computer |
| `rm <name>` | Delete the container and its volume. Ask for the name again |
| `restart <name>` | Restart the container. The volume stays |
| `rename <name> <new>` | Rename. Portals stay claimed; the SSH username changes |
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

Keys:

| Command | Effect |
| --- | --- |
| `pair` | Print a one-time password for another client |
| `key ls` / `key rm` | List or remove bound client keys |
| `key copy` | Print the server's GitHub public key and nothing else, so it can be copied |
| `env set <name> <value>` | Store an environment variable. Injected into every container at start |
| `env rm <name>` | Remove it. Running containers keep the old value until restart |
| `env ls` | List names only. Values are not printed |
| `whoami` | Which key this session used |
| `defaults` | Default node, image, and size. Non-interactive `new` uses these |

Non-interactive create:

```
ssh box.example.com new web --image base --node home --cpu 2 --memory 2G --disk 20G --json
```

If the chosen node has not pulled the image, `new` tells the operator to run `image pull`. It does not pull implicitly. A pull can take long enough that `new` would look stuck.

On the server machine, `box` talks to the local server process over a localhost socket. It can `pair`, `node pair`, `key ls`, `key rm`, `key copy`, `env set`, `env rm`, `env ls`, and `status`. It does not open a path around the REPL for creating computers. That stays on a bound client, so a person on the server console cannot skip the key check by accident. `status` is the exception: it is read-only.

## Portals

A portal is a hostname routed to one TCP port inside one container. The container claims it. The control REPL does not. This is the same shape as an Amp portal: something inside the machine publishes a port and gets a hostname back. It is not Amp. There is no review widget, no thread, and no login wall. TLS stays on the operator's edge.

`new` does not register a hostname. Until the container claims one, the server's fixed HTTP port has nothing to route for it.

The parent domain is set when the server starts. It is not chosen by the container, and an agent does not invent one. `box domain` prints it.

```
box domain
box.example.com
box portal check web
free
box portal add web 3000
http://web.box.example.com
box portal ls
HOST                 PORT
web                  3000
box portal rm web
```

`check` and `add` take a label, not a full hostname. The server joins the label to the configured domain. `web` becomes `web.box.example.com`. A label cannot contain a dot, so a container cannot claim a name outside that domain. `check` asks whether that hostname is already claimed. It does not claim it. `add` checks again and claims it in one step. If it is taken, `add` refuses and names the container that holds it. A container can hold several labels. Each label points at one port. Two labels may point at the same port.

The guest CLI talks only to the controller, over a socket mounted into that container. The socket is bound to that container. A request cannot name a different container. The controller asks the server to claim the hostname. The server is the only place that knows every claim, including claims on other nodes. If the server accepts, the controller points that hostname's frp HTTP proxy at the container's bridge address and the requested port. If the server refuses, nothing is registered.

The container does not edit frp config. A compromised container that could write that config could take another container's hostname.

Podman stays. `podman run -p` cannot add a host port to a running container, and a portal must be able to target a port that was not known at create time. Rootful Podman gives each container a bridge address. From the host, `container-ip:3000` reaches port 3000 inside that container without a published host port. The controller reads the address from `podman inspect` and uses it as the frp upstream. When the container restarts and the address changes, the controller updates the upstream. No container is recreated to add a portal.

The container must listen on `0.0.0.0` or its bridge address, not only on `127.0.0.1`. A process bound to loopback is not reachable at the bridge address. `add` does not check that the port is listening. It routes, and the operator sees a connection error until something listens. That matches a portal: the route exists before the server is ready.

Removing a container drops its portals. `restart` keeps them. The claim record lives on the server. The route lives on the node.

There is no login wall and no certificate issuance. The operator's edge terminates TLS and forwards HTTP to the server's fixed port. That port is plain. The edge should be the only client allowed to reach it. An unknown Host gets 421.

Non-HTTP ports are not given hostnames. SSH does not go through this port. A database stays inside the container unless the operator publishes it some other way. The first version is HTTP only, because that is what frp's host routing does.

A skill ships in the base image at `/home/box/.agents/skills/box/SKILL.md`. It tells an agent to read the domain with `box domain`, claim a label with `box portal`, listen on `0.0.0.0`, and not to invent a domain or edit proxy config. It also tells the agent to use mise for Go and Rust, and `mise x` for a toolchain that should not stick. The skill does not contain credentials. The domain is not baked into the image. The controller fetches it from the server and answers `box domain` from that.

## Images

The base image is a Fedora computer, not a language runtime. The Dockerfile is `images/base/Dockerfile`. It starts from `fedora:44` and installs systemd as PID 1, which Podman runs with `--systemd=always`.

The image contains:

- systemd, sudo, openssh-server, and CA certificates
- user `box`, uid 1000, passwordless sudo, home `/home/box`
- git, curl, jq, vim, gcc, make, rsync, and the usual shell tools
- agent tools: `rg`, `fd`, `bat`, `fzf`, `zoxide`, `hyperfine`, `tokei`, `sd`, `dust`, and `gh`
- mise, installed from `https://mise.run` to `/usr/local/bin/mise`
- the latest stable Go, stable Rust, the Node LTS, and the latest Python 3, installed with mise's built-in backends into the `box` user's data directory
- sshd listening on 2222, password login off, root login off
- an empty `/etc/machine-id`, so each computer generates its own on first boot
- the skill at `/home/box/.agents/skills/box/SKILL.md`

mise is activated in `~/.bashrc`. A one-off toolchain is `mise x go@1.25 -- go version`. That installs the requested version if it is missing, runs the command, and does not change the default. `mise use` is what changes the default, and it writes the current directory's `mise.toml`.

The image has no client keys and no host key. The controller generates a host key per computer on first start and keeps it on the node, so reconnects do not trip `known_hosts`. Authorized keys are a file the controller writes and the container reads. The computer cannot change the host key.

## GitHub key

The first time the server starts, it generates an ed25519 key pair and stores it next to its SQLite file. The private key never leaves the server. It is not a login key. It is the key the operator adds to GitHub.

`key copy` prints the public key on its own line and nothing else. The same command exists in the control REPL and as `box key copy` on the server. Either output can be pasted into GitHub. A later start reuses the same key. It does not rotate unless the operator deletes the file and starts again.

When a container is created, the controller writes that public key into the container's authorized keys, next to the bound client keys. The container can then be used as a GitHub SSH remote once the operator has added the same public key to GitHub. The private key is not copied in. Signing and pushing as that key is a later step. The first version only makes the public key easy to copy and present.

## Environment

The server stores environment variables. A container does not. `env set GH_TOKEN ghp_...` writes the value on the server. `env ls` prints names, never values. `env rm` deletes a name.

On create and on restart, the controller asks the server for the current set and passes it to `podman run` as `--env`. The value is not written into the image, the volume, or a file the container can read back as a store. It is in the process environment, which is what `gh` reads. `GH_TOKEN` takes precedence over stored `gh` credentials, so a container with that variable does not run `gh auth login`.

A running container does not see a change until restart. The controller does not edit the environment of a live container. `env set` says that.

The set is global to this deployment. It is not per container. A private deployment has one operator, and the point is to avoid repeating a login on every computer. A value is a secret. The REPL does not echo it back. The SQLite file is the secret store, so that file is not world-readable.

Build and publish:

```
docker build -t base:local images/base
crane push base:local registry.example.com/base:latest
```

The registry is the operator's. The server only stores the reference. `image pull` runs `podman pull` on the node. A node that has never pulled `base` cannot create a computer from it. That is deliberate: a node is not usable until it has pulled an image.

## One binary

The project builds one executable, `box`. The subcommand selects the role. There is no `boxd` and no `box-node`.

| Command | Where it runs | What it does |
| --- | --- | --- |
| `box serve` | the server machine | SSH entry, HTTP routing, SQLite, the pairing and env store |
| `box node` | a deploy node | the controller. Starts frpc, calls Podman, serves the guest socket |
| `box node join` | a deploy node | pair, then keep running as the controller |
| `box` | inside a container | `domain`, `portal`. Talks to the guest socket. Refuses `serve` and `node` |
| `box` | on the server machine, not serving | `pair`, `key`, `env`, `status`, over the localhost socket |

A release is one archive per OS and architecture. The server, the node, and the image all download that archive. The image Dockerfile copies the binary to `/usr/local/bin/box`. It does not build it.

## Controller

`box node` runs as root. Podman is rootful. Rootless mode needs a systemd user session and cgroup delegation, which fails on a machine that was only SSH'd into. Rootful is the smaller setup for a private node. The container is still unprivileged inside: no `--privileged`, no host network, no Docker socket.

1. Pair with the server. Store the node token.
2. Start frpc and register the RPC and HTTP proxies. Register an STCP proxy per running computer.
3. Serve loopback RPC.
4. Call `podman` for each command.
5. Report cpu, memory, free disk, and pulled images on heartbeat.

Join:

```
box node join --server box.example.com:7000 --code <pairing-code> --name home
```

The code is single use. The node id and token are written to `/var/lib/box/node.json`. A restart is `box node`, which reads that file and does not ask for the code again.

Creating a computer:

1. Refuse if the image is not in the local cache.
2. `podman volume create web`. This volume is the computer's disk. `podman rm` does not delete it. `rm` in the REPL deletes the container and then the volume.
3. `podman run -d --name web --systemd=always --restart=always` with `--cpus`, `--memory`, and the volume mounted at `/var/lib/box`. Home, sshd state, and anything the user installs under `/var/lib/box` live on that volume. The image's own upper layer is not the disk.
4. Publish container port 2222 on `127.0.0.1` only. Do not publish an HTTP host port. A portal targets the container's bridge address, which can reach any port the container listens on.
5. Write the host key and the current authorized keys onto the volume. Start sshd via the image's systemd unit.
6. Register the STCP proxy for port 2222. Register no HTTP host until the container claims one.

`restart` is `podman restart`. The volume is not recreated. `resize` for cpu and memory is `podman update`. Growing the disk is `podman volume` quota where the filesystem supports it, otherwise a new volume and a copy. Shrinking is refused.

If the controller restarts, it asks `podman ps -a` which computers exist, rebinds their loopback ports, and reports status. The disk is the volume.

If the node lacks capacity, the RPC returns a specific error. The server shows it and names another node if one has the image. The controller does not place computers itself.

## Placement

`new` without `--node` uses the default node. If there is no default and only one node is online, that node is used. If more than one is online, `new` asks.

A computer on an offline node is shown as `offline`. It is not moved. Moving a volume is out of scope. The operator can `new` a different computer on another node.

The server will not place a computer on a node that has not pulled the image.

## Data

The server keeps one SQLite file.

- `keys`: public key, comment, bound at
- `pairings`: hash of a one-time password or node code, expiry, used at
- `nodes`: name, tags, last heartbeat, capacity
- `images`: name, ref, whether it is the default
- `computers`: name, node, image, size, state
- `portals`: hostname, container, node, port, claimed at
- `env`: name, value. The value is not returned by list commands
- `shares`: computer, key, web or ssh. The owner key is implicit

Computer names are globally unique, because the SSH username is that name. `new` refuses a collision. A portal hostname is also globally unique. It is not derived from the container name.

The controller keeps its own local record: node token, volume name, published ports, host key path. The server does not store those.

## Processes

On the server, one process supervisor runs `box serve` and `frps`.

| Process | Listens | Role |
| --- | --- | --- |
| `box serve` | 22, the fixed HTTP port, and a localhost socket | SSH control plane, key check, TCP splice, Host routing |
| `frps` | 7000, token required | Meeting point for nodes |

`frps` is not for clients. 7000 requires the token issued at pairing. `box` on the server machine uses the localhost socket. It does not open SSH to itself.

On a node, `box node` is the only process the operator starts. It starts frpc and calls Podman. The node needs Podman and a cgroup v2 host. It does not need KVM.

## Not in the first version

- accounts, billing, invites, teams, SSO
- email
- a web coding agent
- secret injection at the edge
- a public IP per computer
- a hardware VM boundary
- TLS and public access control on the HTTP port
- moving a computer between nodes
- CDN

## Dependencies

| Piece | Choice | Why |
| --- | --- | --- |
| Client | OpenSSH, already installed | No client to ship |
| Control SSH | charmbracelet/wish | An sshd we can branch on the username |
| Computer SSH | openssh-server in the image | A normal sshd, so scp and Remote-SSH work |
| Server to node | frp | NAT traversal, encryption, TCP splice, HTTP host routing |
| Computer | Podman, rootful | One less runtime. Persistent volumes, systemd, resource limits |
| Base image | fedora:44 | Current stable Fedora, not Debian |
| Toolchain | mise | Built-in Go and Rust backends, plus `mise x` for a temporary toolchain |
| Server state | SQLite | One file, enough for a private deployment |

The `box` binary and the skill are the code to write. The rest is the software above.
