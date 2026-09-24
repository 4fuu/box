---
name: box
description: Manage this computer's portal hostnames, server environment, and mise toolchains. Use when an agent needs a public hostname, a Go, Rust, Node, or Python version, or a one-off tool without changing the computer's default toolchain.
---

# box

This computer is a container. The domain and the toolchains are already here. Do not invent a domain. Do not install Go or Rust from their upstream installers.

## Domain

Read the domain the server was configured with:

```
box domain
```

That prints a parent domain such as `box.example.com`. Claim a label under it:

```
box portal check web
box portal add web 3000
```

`web` becomes `web.box.example.com` and routes to port 3000 in this container. A label cannot contain a dot. `check` does not claim. `add` refuses if the label is taken.

The process must listen on `0.0.0.0`, not only `127.0.0.1`. List claims with `box portal ls`. Remove one with `box portal rm web`.

## Tools

`rg`, `fd`, `bat`, `fzf`, `jq`, `gh`, and the other agent tools are already on `PATH`. Do not install another copy.

`gh` is authenticated when the server has `GH_TOKEN` set. Do not run `gh auth login`. If `gh` says it is not logged in, the server has no token. Say so. Do not ask the user to paste a token into the container.

## Toolchain

`go`, `rustc`, `node`, and `python3` are on `PATH` through mise. The defaults are the latest stable Go, stable Rust, the Node LTS, and the latest Python 3.

Run a command with a different toolchain without changing the default:

```
mise x go@1.25 -- go version
mise x rust@1.81 -- cargo build
mise x node@22 -- node --version
mise x python@3.12 -- python3 --version
```

`mise x` installs that version if it is missing, runs the command, and leaves the default alone.

Change the default only when the project needs it:

```
mise use go@1.25
mise use rust@1.81
```

That writes the current directory's `mise.toml`. Do not use `--global` unless the user asked to change the computer's default.

See `mise ls` for what is installed. See `mise x --help` for the one-off form.
