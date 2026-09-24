---
name: box
description: Manage this computer's portal hostnames and its mise toolchains. Use when an agent needs a public hostname, a Go or Rust version, or a one-off tool without changing the computer's default toolchain.
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

## Toolchain

`go` and `rustc` are on `PATH` through mise. The defaults are the latest stable Go and stable Rust.

Run a command with a different toolchain without changing the default:

```
mise x go@1.25 -- go version
mise x rust@1.81 -- cargo build
```

`mise x` installs that version if it is missing, runs the command, and leaves the default alone.

Change the default only when the project needs it:

```
mise use go@1.25
mise use rust@1.81
```

That writes the current directory's `mise.toml`. Do not use `--global` unless the user asked to change the computer's default.

See `mise ls` for what is installed. See `mise x --help` for the one-off form.
