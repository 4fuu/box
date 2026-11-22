## Portals

- box forwards portal traffic as-is. It no longer answers with its own
  `502` when a computer is offline or a stream fails; the connection is
  aborted and the edge in front of box reports the failure.
- Keep-alive is honored end to end: one tunnel connection to a computer
  serves many requests, instead of a fresh connection per request.
- A request that arrives while its computer reconnects is held for up to
  3 seconds and then served, instead of failing.
- Fixed: reclaiming a portal label on a new port kept forwarding to the
  old port.

## Tunnels

- A tunnel that dies without a close packet is noticed within 30 seconds
  instead of 2 minutes.
- The agent's reconnect delay starts over after a tunnel stays up for
  5 seconds, so a link that keeps dropping reconnects at once instead of
  waiting out the longest delay.
- Opening a portal stream fails at once when the stream limit is reached,
  instead of hanging; the limit is raised from 100 to 512 concurrent
  streams.

## SSH sign-in

- A connection may offer 6 keys (sshd's default) instead of 3, so a key
  ring with several keys reaches the bound key on one connection.
- An address may fail 20 key offers in 5 minutes before a temporary ban,
  up from 5, so an honest mistake behind a shared address does not ban
  everyone there.
- A transient database error no longer counts as a key guess.
