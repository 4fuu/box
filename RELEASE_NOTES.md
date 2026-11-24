## Two-node operation

- Two servers can serve the same HTTP portal domains without an external database.
  One server writes configuration and replicates it to the other server's local
  SQLite database. Either server can accept portal traffic and forward it to the
  server holding the computer's tunnel.
- Computer agents receive both QUIC endpoints, race their handshakes, and try the
  other endpoint if a server goes down. In-flight connections can still drop.
- Join computers at the configuration writer's hostname. If the writer is down,
  the replica can serve existing portals, but configuration changes are unavailable.
  SSH is not forwarded between servers, and event logs and blobs remain local.
  See [two-node setup](https://github.com/4fuu/box/blob/main/docs/install.md#two-nodes).

## Portals

- Servers can serve multiple domains. Use `box domain` to list them and pass
  `--domain <domain>` when checking, adding, or removing a portal on a server
  with more than one configured domain. A portal label is unique per domain.
