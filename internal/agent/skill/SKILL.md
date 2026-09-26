---
name: box
description: Claim a public hostname for a process on this machine. Use when an agent needs the server domain or a portal label.
---

# box

This computer is a machine. `box` talks to the local agent on this machine. This skill has no credentials.

## Domain

```
box domain
```

That prints the parent domain, such as `box.example.com`.

## Portals

A label has no dot. Check, claim, list, and remove with:

```
box portal check web
box portal add web 3000
box portal ls
box portal rm web
```

`check` does not claim. `add` refuses when the label is taken. `ls` lists claims. `rm` releases one.

`web` becomes `web.box.example.com` and routes to port 3000. The process listens on `127.0.0.1`.
