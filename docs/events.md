# Events and blobs

## Events

A durable log in the server's SQLite file. An event has an id, topic, body, from label, time, and a key when the publisher sent one. Ids are 64-bit, server-assigned, and only increase; they never repeat, not across restarts, not after pruning. The server keeps at most 100000 events and nothing older than 7 days. Pruning runs on the write path, amortised over inserts, with index-driven deletes. Constants, not flags.

- Topic: 1–255 bytes, segments separated by `/`, each segment `[A-Za-z0-9_.-]+`. No empty segments, so no leading or trailing `/` and none doubled. `#`, `+`, and whitespace never appear in a published topic.
- Body: any bytes, including newlines and binary, at most 1 MiB (1048576). Empty is allowed. 1 MiB is a protection cap, not a format rule.
- Key (optional): 1–128 bytes of printable ASCII without spaces. When the same `from` publishes the same key while the original is still retained, the original event comes back — same id, `duplicate` set — and nothing is stored. One transaction behind a unique index makes this race-free. A key expires with its event.

| Door | `from` |
| --- | --- |
| `http://event.<domain>/api/events` — a device that speaks HTTP, for any configured domain. Access token required | the token's comment, when it is a valid label; else `token-<id>` |
| `event pub` / `event get` — the control REPL | `ssh` |
| `box event` on the server, over the localhost socket | `server` |
| `box event` on a computer — agent socket, then the control stream | the computer's name |

A client never names its own `from`; a `from` in a JSON body is ignored. Give each device its own token with a meaningful comment: `box token add door-sensor`.

### Reading

`GET /api/events?since=&topic=&from=&limit=&wait=`. `since` is an id cursor, default 0. `topic` and `from` are repeatable; each list is a union. A topic filter is an exact topic, or a prefix ending in `/#`: `site1/#` matches `site1/a` and `site1/a/b`, not `site1` itself and not `site10/x`. A bare `#` matches everything. Prefix filters walk an index range, never `LIKE`. In a URL, `#` is written `%23`: `topic=site1/%23`. `limit` is 1–1000, default 100. Results are ordered by id ascending, ids strictly greater than `since`, at most `limit`.

Every read response carries the window, so a client can detect gaps and start from now:

```json
{"events":[...],"oldest":123,"latest":456,"more":false}
```

`oldest` is the smallest retained id, `latest` the largest id ever assigned; both are 0 on an empty log, and `latest` survives pruning and restarts. A client whose `since` is below `oldest-1` missed events. `more` is set when `limit` cut the result.

With `wait` (0–25 whole seconds, default 0) and no matching event after `since`, the read blocks until a matching publish or the wait ends, then answers — possibly empty, always with the window. A publish broadcasts in-process; each wake-up re-queries SQLite; no goroutine polls the database. The wait ends early when the client leaves, the control stream closes, or the server stops. 25 seconds stays under the agent's 30-second call timeout to the server. Concurrent waiters are not capped; each holds one goroutine and one open request.

A body that is valid UTF-8 is a JSON string in `body`; any other bytes are base64 in `body_b64`. An empty body carries neither field.

### Publishing

- `POST /api/events` with JSON `{"topic","body","key"?}`. Binary bodies use `"body_b64"` instead of `"body"`; sending both is 400. A `from` field is ignored. Answers `201 {"id":n}`; a dedup hit answers `200 {"id":n,"duplicate":true}`.
- `POST /api/events/<topic>` with any body and any content type: the request bytes are stored verbatim. This is the one-line path for tiny devices. The key rides `Idempotency-Key` or `?key=`. Same answers.
- A body over 1 MiB is 413. A bad topic, key, or filter value is 400 with a one-line reason. Larger payloads belong in a blob, named in the body; see Blobs below.

For every configured domain, `event.<domain>` answers only `/api/events`, `/api/events/...`, `/api/blobs`, and `/api/blobs/...`, and never redirects to sign-in. Computers use `box event`, not the HTTP API.

### Small devices

Ids are 64-bit. Store the cursor in a 64-bit integer; a 32-bit counter (some ArduinoJson configurations) wraps. Two patterns with curl:

```bash
curl -H "X-Box-Token: $TOKEN" -H "Idempotency-Key: door-1" \
  -d 'open' http://event.box.example.com/api/events/kitchen/door

while true; do
  curl -s -H "X-Box-Token: $TOKEN" \
    "http://event.box.example.com/api/events?since=$SINCE&topic=kitchen/%23&wait=25"
  # advance SINCE to the last id seen, or to latest on the first answer
done
```

On a computer, `box event get --follow` runs that loop and prints one line per event; `box event get --since 0` replays what is retained.

## Blobs

Big payloads do not go through events. A device stores the bytes as a blob, then publishes an event whose body names it: `blob:<id>`. No event field exists for this; it is a convention.

- `POST /api/blobs` on `event.<domain>`, access token required. The request bytes are stored verbatim; the `Content-Type` rides along. Answers `201 {"id":n,"size":s}`. `from` is the token's comment, as with events.
- `GET /api/blobs/<id>` on `event.<domain>`. Answers the bytes with the stored type. No listing, no ranges, no delete: retention is the only removal.

A blob is at most 64 MiB, answered 413 over. All blobs together are at most 4 GiB, and none is older than 7 days; pruning runs on the write path, oldest first, and `from`, size, and type live in the server's SQLite file while the bytes live in the configured backend. Constants, not flags. The event that names a blob can outlive it: a fetch that arrives too late is a 404, not a publisher error.

Backends, chosen at `box serve`:

| Backend | Flags |
| --- | --- |
| `disk`, the default | `--blob-dir <path>`, default `<data>/blobs` |
| `s3`, any S3-compatible service: OSS, R2, MinIO, B2, S3 | `--blob-s3-endpoint <host>`, `--blob-s3-bucket <name>`, `--blob-s3-region <region>`, and `--blob-s3-pathstyle` for MinIO and on-prem |

S3 credentials are environment variables, not flags, so they stay out of process listings: `BOX_BLOB_S3_KEY` and `BOX_BLOB_S3_SECRET`. Each row records which backend holds its bytes: after a switch, the old backend's blobs answer 410, and their objects are left for manual cleanup.
