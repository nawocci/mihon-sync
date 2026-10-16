# mihon-sync

The self-hosted sync server for Kioku's cloud sync. It is a dumb, per-library log
with last-writer-wins semantics: devices push opaque records stamped with a hybrid
logical clock, the server compares markers, and everyone pulls the winners. The
server never parses or understands library data.

One API key is one library. All of a user's devices share the same key.

## Quick start

```sh
docker compose up -d          # builds locally
# or track the published image instead of building:
docker compose pull && docker compose up -d
```

Every push to `main` builds and pushes a multi-arch image to
`ghcr.io/nawocci/mihon-sync` (`latest` + `sha-…`); `v*` tags publish semver
tags. All data lives in the `mihon-sync-data` volume.

Open `http://<host>:8080` for the dashboard. From a browser on the same machine
(or through a local reverse proxy), it offers to generate the first key. From
anywhere else, keys come from invites:

```sh
docker exec mihon-sync mihon-sync invite create --data /data
```

Redeem the printed `inv_...` code on the dashboard's "Have an invite?" form to
get a `mhk_...` key for a new library. The key is shown once; the dashboard keeps
it in that browser's local storage.

Then on the phone: Settings → Sync → paste the server URL and key (or scan the
pairing screen).

### Bootstrap network policy

`--bootstrap-network` decides who may generate keys on the web:

- `loopback` (default): only requests arriving from 127.0.0.1, e.g. through a
  local reverse proxy. Use `docker exec ... invite create` for everything else.
- `private`: also local/LAN addresses. Convenient when you first set the server
  up from another device; do not leave this on while the port is exposed.
- `any`: no restriction. Only for throwaway setups.

## CLI

```
mihon-sync serve       run the server (default when no command is given)
mihon-sync invite create [--label X] [--uses N] [--ttl 168h] [--data DIR]
mihon-sync version
```

`--data` points at the directory holding `sync.db`; it is created if missing.

## API

All endpoints take `Authorization: Bearer mhk_...` except health, register and
the dashboard's bootstrap endpoints.

| Endpoint | Purpose |
| --- | --- |
| `GET /` | dashboard |
| `GET /v1/health` | liveness |
| `POST /v1/register` | redeem an invite: `{inviteCode, label}` → `{apiKey, label, createdAt}` |
| `GET /v1/me` | `{label, createdAt, recordCount}` for the key's library |
| `POST /v1/sync/push` | `{records: [envelope]}` → `{results: [{type, key, accepted, marker}]}` |
| `GET /v1/sync/pull?since=&limit=` | `{records: [envelope], watermark, hasMore}` |

An envelope is:

```json
{
  "type": "manga",
  "key": "12345|/manga/some-title",
  "marker": { "wallTimeMillis": 1712345678901, "counter": 3, "deviceId": "uuid" },
  "deleted": false,
  "payload": "base64 protobuf"
}
```

`seq` is per library and strictly increasing. Every accepted write — including an
update to an existing key and a deletion — gets a fresh `seq` and re-enters the
pull stream, so a device that was offline converges as soon as it pages through
its watermark. Deletions are ordinary records with `deleted: true` and no payload;
they persist indefinitely.

## Security notes

- API keys and invite codes are stored hashed (SHA-256) and shown exactly once.
- Payloads are opaque to the server; TLS termination is the reverse proxy's job.
- Keys are "users", not admins: one key cannot mint another key.
- The server keeps no logs of record contents, only counts.

## Not in this version

- WebSocket hints for push notifications while the app is open (clients poll).
- QR rendering on the dashboard (the app can still be paired by pasting).
- Key rotation for leaked keys.
- Tombstone compaction.

## Legacy

The previous server (revision-based sync with its own account system) is kept
on the `legacy` branch.
