# Portals and access tokens

## Portals

A portal is a hostname routed to one TCP port on one computer. The computer claims it; the REPL does not.

`check`, `add`, and `rm` take a label and a domain. The server serves one or more parent domains (`box domain` lists them), and the portal host is the label joined to the chosen domain. With one domain the choice is implied; with several, pass `--domain <domain>`. A label cannot contain a dot; `event` and `auth` are reserved. `add` claims atomically or refuses and names the holder. A computer can hold several labels, each on one port, and the same label can live on different domains under different computers. Hostnames are globally unique and not derived from the computer's name.

`box portal add <label> <port> [public|private] [--domain <domain>]` is public; appending `private` requires an access token. Claiming the same label on the same domain again changes that. Existing rows stay public. `box portal ls` prints each claim's full hostname, so a claim is unambiguous when the server serves several domains.

A request with a claimed `Host` opens a `portal` stream, which the agent connects to `127.0.0.1:<port>`, so the process only needs loopback. The route exists before anything listens. The printed URL carries no port; the edge decides the reachable one.

The server forwards the request as the client sent it and answers with what the computer sends. When no stream can be opened, the request is aborted instead of answered, so the client sees the connection end and the server invents no status. If the stream fails while forwarding a request, the server does not resend it; the client decides whether to try again. Before forwarding, the server waits up to 3 seconds if the computer has no live tunnel.

A private portal checks the token before opening the stream. A browser navigation (`Accept` contains `text/html`) without one is redirected to `http://auth.<domain>/` for that portal's domain, which sets an HttpOnly cookie on the same parent domain once a valid token is submitted. Other clients send `X-Box-Token` or `Authorization: Bearer` and get 401 without it. The server strips that cookie, and a bearer token that is one of its access tokens, before forwarding. One token opens every private portal; a browser signs in once per parent domain.

With two nodes, an entry that does not hold the computer's tunnel forwards the request once to its peer over the private node link. The peer applies the same access rule and proxies to the computer. The request keeps its `Host`, body, and streaming response; the entry does not retry after forwarding. When neither node has the tunnel, the entry returns 503. A portal claim made by a computer attached to the replica is forwarded to the writer, and the replica serves it after the next snapshot.

Removing a computer drops its portals. Claims live on the server; routes live while the tunnel does.

## Access tokens

Only the server creates them. `token add [comment]` prints a token with no expiry; `--for 12h` sets a lifetime (`30d` and Go durations such as `720h` work). `token ls` prints tokens again, and the TUI tokens screen keeps them on screen, so the operator can copy them any time. `token rm <id>` revokes one. A revoked or expired token stops opening private portals and the event API, including through a cookie the browser already holds. Never log a token.

Give each device its own token with a meaningful comment: `box token add door-sensor`. The comment becomes the event's `from` when the device publishes over HTTP; see [events](events.md).
