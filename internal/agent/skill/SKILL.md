---
name: box
description: Claim a hostname for a process on this machine, or publish and read events. Use when an agent needs the server domain, a portal label, or the event log.
---

# box

This computer is a machine. `box` talks to the local agent on this machine. This skill has no credentials.

## Domain

```
box domain
```

That prints the parent domain, such as `box.example.com`.

## Portals

A label has no dot. `event` and `auth` are reserved for the server. Check, claim, list, and remove with:

```
box portal check web
box portal add web 3000
box portal add lock 3000 private
box portal ls
box portal rm web
```

`check` does not claim. `add` refuses when the label is taken. The same computer can claim a label again to change the port or the access. `ls` lists claims. `rm` releases one.

`web` becomes `web.box.example.com` and routes to port 3000. The process listens on `127.0.0.1`.

`box portal add <label> <port>` is public. `private` requires an access token created on the server. This machine does not create tokens. A browser without a token is sent to `auth.<domain>`. Other clients send `X-Box-Token` or `Authorization: Bearer`.

## Events

`box event` publishes and reads the server's durable event log. The computer's name is the event's `from`. This command does not use the HTTP API and does not take a token.

```
box event pub door open
box event pub door --key door-1
box event get --since 0
box event get --topic door/#
box event get --follow --topic door
echo '{"temp":21}' | box event pub sensor1
```

Text arguments join into the body; with no text, the body comes from stdin. `--key` deduplicates: a retry while the original is retained returns the same id. `get` prints one line per event, bodies escaped so they stay on one line; `--json` prints the whole answer including `oldest`, `latest`, and `more`. `--follow` long-polls and prints new events until interrupted. `--topic` accepts `prefix/#`. Ids are 64-bit; keep the cursor in a 64-bit integer.

Devices that are not this computer use `http://event.<domain>/api/events` with their own access token; the token comment names the device. This skill does not contain that token.
