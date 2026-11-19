---
name: box
description: Claim a hostname for a process on this machine, or publish and read events. Use when an agent needs the server domain, a portal label, or the event log.
---

# box

This computer is a machine. `box` talks to the local agent on this machine. This skill has no credentials. It is a short guide; run `box help` for the full syntax and what each flag means.

## Domain

```
box domain
```

Prints the parent domain, such as `box.example.com`. Portal labels join it.

## Portals

Claim a hostname for a process that listens on `127.0.0.1`:

```
box portal check web
box portal add web 3000
box portal add lock 3000 private
box portal ls
box portal rm web
```

`web` becomes `web.box.example.com` and routes to port 3000. A label has no dot; `event` and `auth` are reserved. `add` is public by default; `private` needs an access token created on the server — this machine does not create tokens.

## Events

Publish to and read the server's durable event log, to coordinate with other machines and devices. The computer's name is the event's `from`; publishing here needs no token.

```
box event pub door open
box event get --since 0
box event get --follow --topic door
```

`box help` covers the body formats, dedup keys, topic filters, and output options.
