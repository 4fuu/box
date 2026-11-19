# Portals and access tokens

## Portals

A portal is a hostname routed to one TCP port on one computer. The computer claims it; the REPL does not.

`check` and `add` take a label; the server joins it to the domain set at server start (`box domain` prints it). A label cannot contain a dot; `event` and `auth` are reserved. `add` claims atomically or refuses and names the holder. A computer can hold several labels, each on one port. Hostnames are globally unique and not derived from the computer's name.

`box portal add <label> <port>` is public; appending `private` requires an access token. Claiming the label again changes that. Existing rows stay public.

A request with a claimed `Host` opens a `portal` stream, which the agent connects to `127.0.0.1:<port>`, so the process only needs loopback. The route exists before anything listens. The printed URL carries no port; the edge decides the reachable one.

A private portal checks the token before opening the stream. A browser navigation (`Accept` contains `text/html`) without one is redirected to `http://auth.<domain>/`, which sets an HttpOnly cookie on the parent domain once a valid token is submitted. Other clients send `X-Box-Token` or `Authorization: Bearer` and get 401 without it. The server strips that cookie, and a bearer token that is one of its access tokens, before forwarding. One token opens every private portal.

Removing a computer drops its portals. Claims live on the server; routes live while the tunnel does.

## Access tokens

Only the server creates them. `token add [comment]` prints a token with no expiry; `--for 12h` sets a lifetime (`30d` and Go durations such as `720h` work). `token ls` prints tokens again, and the TUI tokens screen keeps them on screen, so the operator can copy them any time. `token rm <id>` revokes one. A revoked or expired token stops opening private portals and the event API, including through a cookie the browser already holds. Never log a token.

Give each device its own token with a meaningful comment: `box token add door-sensor`. The comment becomes the event's `from` when the device publishes over HTTP; see [events](events.md).
