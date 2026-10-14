<p align="center">
  <img src="assets/logo.svg" width="120" height="120" alt="mihon-sync logo">
</p>
<h1 align="center">mihon-sync</h1>

Self-hosted sync backend for [Mihon](https://mihon.app)-based apps and
forks. Syncs your library, chapter read/bookmark progress, categories,
reading history, hidden scanlators and app preferences across devices.

Single static Go binary + embedded SQLite. No other services required.

## How it works

- Revision-based delta sync: the server keeps a monotonically increasing
  revision per account; clients push local changes and pull
  `changes since revision N`.
- An **API key identifies an account**. Share one key across your devices to
  sync them. Keys are stored SHA-256-hashed; the plaintext is shown once at
  creation.
- Entity identity is stable across devices: manga are keyed by
  `(source_id, url)`, chapters by `(manga, chapter url)`, categories by name
  — the same rules Mihon's backup restore uses.
- Conflict resolution mirrors Mihon's backup restore semantics:
  - manga: higher client `sync_version` wins, `favorite` is OR-merged,
    `favorite_at` takes the earliest known date, deletions stick unless a
    newer version re-adds
  - chapters: `read`/`bookmark` OR-merged, furthest `last_page_read`, and
    Updates-page dates take the **earliest known** value so a fresh device's
    bulk fetch converges back to the originals
  - history: max `read_at` / `read_duration`
  - categories / preferences / scanlators / stores: last write wins
  - memo blobs: shallow key-wise union, incoming wins on a conflict
- Deletions propagate as tombstones, garbage-collected after
  `MIHON_SYNC_RETENTION_DAYS` (default 30).
- Writes that would not change anything are skipped, so an applied change is
  not echoed back to the other devices forever.

## Run with Docker

Pre-built images (amd64/arm64) are published to
[ghcr.io/nawocci/mihon-sync](https://github.com/nawocci/mihon-sync/pkgs/container/mihon-sync).

```sh
curl -O https://raw.githubusercontent.com/nawocci/mihon-sync/main/docker-compose.yml
docker compose up -d
docker compose exec mihon-sync /mihon-sync genkey -label "my devices"
```

Or without compose:

```sh
docker run -d -p 8080:8080 -v mihon-sync-data:/data ghcr.io/nawocci/mihon-sync:latest
```

To build the image locally instead, use `docker compose up -d --build`
(or `docker build -t mihon-sync .`).

## Run from source

Requires Go 1.26+.

```sh
go build ./cmd/mihon-sync
./mihon-sync genkey -label "my devices"
./mihon-sync serve
```

## Configuration (environment variables)

| Variable                    | Default           | Description                                              |
| --------------------------- | ----------------- | -------------------------------------------------------- |
| `MIHON_SYNC_ADDR`           | `:8080`           | Listen address                                           |
| `MIHON_SYNC_DB`             | `./mihon-sync.db` | SQLite database path                                     |
| `MIHON_SYNC_RETENTION_DAYS` | `30`              | Tombstone retention before GC                            |
| `MIHON_SYNC_API_KEY`        | —                 | Bootstrap key; account ensured on serve start            |
| `MIHON_SYNC_REGISTRATION`   | `open`            | Key generation mode: `open`, `invite`, or `closed`       |
| `MIHON_SYNC_MAX_ENTITIES`   | `500000`          | Per-account entity cap; `0` disables                     |
| `MIHON_SYNC_METRICS`        | `false`           | Expose `GET /metrics` in Prometheus text format          |
| `MIHON_SYNC_LOG_LEVEL`      | `info`            | `debug`, `info`, `warn`, or `error`                      |

Public hosts should set `MIHON_SYNC_REGISTRATION=invite`.
Then mint one-time codes (default 15 minute window) with:

```sh
docker compose exec mihon-sync /mihon-sync invite -label "friend"
```

The legacy `MIHON_SYNC_ALLOW_REGISTRATION=false` still maps to `closed`.
Put the server behind a reverse proxy with TLS (Caddy, nginx, Traefik…) if
it is reachable from the internet — API keys are bearer credentials.

## Web Dashboard

`mihon-sync` includes a built-in, responsive web dashboard served at `/`:
- **Statistics & Metrics**: Real-time overview of library manga, chapter read progress, categories, history, preferences, excluded scanlators, extension stores and connected devices.
- **API Key & Auth**: Simple login with existing API key, or key generation (registration). In `invite` mode, generation requires a one-time invite code from the operator.
- **Quick Setup**: 1-click copy for Server URL and API Key, setup instructions, and QR code view.
- **Theme Support**: Dark, light, and system theme switching.
- **Account Actions**: Revoke API key and wipe synced data with confirmation.

## Key management

```sh
mihon-sync genkey -label "phone + tablet"   # create (prints key once)
mihon-sync listkeys                         # list key hashes/labels
mihon-sync revokekey mhk_...                # delete account + all its data
```

Add `-json` to `genkey`, `listkeys`, `invite` and `listinvites` for
machine-readable output. When running in Docker:
`docker compose exec mihon-sync /mihon-sync genkey`.

### Invites

An invite code is single-use and expires (default 15 minutes, max 24 hours).
It is only consumed when an account is successfully created; a failed
attempt leaves it valid.

```sh
mihon-sync invite -label "friend" -ttl 15m   # mint one-time code (prints once)
mihon-sync listinvites                       # status: pending / used / expired
mihon-sync revokeinvite 3                    # delete an unused code by ID
```

Only the SHA-256 hash of a code is stored. `listinvites` never shows the code.

## HTTP API

All endpoints except `/healthz`, `/api/v1/info`, `/api/v1/auth/register`, and `/` require `Authorization: Bearer <key>`.

| Endpoint                        | Description                                    |
| ------------------------------- | ---------------------------------------------- |
| `GET /`                         | Embedded Web Dashboard                         |
| `GET /healthz`                  | Liveness probe                                 |
| `GET /metrics`                  | Prometheus metrics (when enabled)              |
| `GET /api/v1/info`              | Server capabilities & registration status      |
| `POST /api/v1/auth/register`    | Generate new API key; in `invite` mode requires `{"invite_code": "mhi_..."}` |
| `GET /api/v1/auth/check`        | Validate the API key                           |
| `DELETE /api/v1/auth/account`   | Revoke current key and delete account data     |
| `POST /api/v1/sync/push`        | Push a batch of changes; returns new revision  |
| `GET /api/v1/sync/pull?since=N` | Changes with revision > N (incl. tombstones)   |
| `GET /api/v1/sync/status`       | Revision + entity counts for the account       |

Errors are returned as `{"error": {"code": "...", "message": "..."}}` so a
client can branch on `code` without parsing prose.

### Push example

```sh
curl -X POST -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  http://localhost:8080/api/v1/sync/push -d '{
    "device_id": "phone-uuid",
    "changes": {
      "mangas": [{"source_id": 7, "url": "/manga/one", "title": "One",
                  "favorite": true, "favorite_at": 1717000000000,
                  "sync_version": 5}],
      "chapters": [{"manga_source_id": 7, "manga_url": "/manga/one",
                    "url": "/c/1", "read": true, "last_page_read": 12,
                    "sync_version": 3}],
      "categories": [{"name": "Reading", "order": 1}],
      "manga_categories": [{"manga_source_id": 7, "manga_url": "/manga/one",
                            "category": "Reading"}],
      "history": [{"manga_source_id": 7, "manga_url": "/manga/one",
                   "chapter_url": "/c/1", "read_at": 1717000000000,
                   "read_duration": 300000}],
      "excluded_scanlators": [{"manga_source_id": 7, "manga_url": "/manga/one",
                               "scanlator": "Group A"}],
      "preferences": [{"key": "theme", "type": "string", "value": "dark"}]
    }
  }'
# → {"rev": 42, "changes": { ...other devices' changes since `since`... }}
```

### Pull example

```sh
curl -H "Authorization: Bearer $KEY" "http://localhost:8080/api/v1/sync/pull?since=41"
# → {"rev": 42, "changes": { ...only entities changed after revision 41... }}
```

Nullable timestamps (`favorite_at`, `date_fetch`, `date_upload`, `read_at`)
are sent as JSON `null`, never `0`. Preference values are JSON with a `type`
tag: `int`, `long`, `float`, `string`, `boolean`, or `stringset`.

## Development

```sh
go test ./...
go vet ./...
```

## Client

[Kioku](https://github.com/nawocci/kioku), a Mihon fork, is the reference
client implementation: its built-in sync engine talks to the HTTP API
documented above. Other Mihon-based forks can integrate against the same
endpoints.
