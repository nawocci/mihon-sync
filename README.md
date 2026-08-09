<p align="center">
  <img src="assets/logo.svg" width="120" height="120" alt="mihon-sync logo">
</p>
<h1 align="center">mihon-sync</h1>

Self-hosted sync backend for [Mihon](https://mihon.app)-based apps and
forks. Syncs your library, chapter read/bookmark progress, categories,
reading history, and app preferences across devices.

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
  - manga: higher client version wins, `favorite` is OR-merged, deletions
    stick unless a newer version re-adds
  - chapters: `read`/`bookmark` OR-merged, furthest `last_page_read`
  - history: max `last_read` / `read_duration`
  - categories / preferences: last write wins
- Deletions propagate as tombstones, garbage-collected after
  `MIHON_SYNC_RETENTION_DAYS` (default 30).

## Run with Docker

```sh
docker compose up -d --build
docker compose exec mihon-sync /mihon-sync genkey -label "my devices"
```

Or without compose:

```sh
docker build -t mihon-sync .
docker run -d -p 8080:8080 -v mihon-sync-data:/data mihon-sync
```

## Run from source

Requires Go 1.24+.

```sh
go build ./cmd/mihon-sync
./mihon-sync genkey -label "my devices"
./mihon-sync serve
```

## Configuration (environment variables)

| Variable                   | Default             | Description                                   |
| -------------------------- | ------------------- | --------------------------------------------- |
| `MIHON_SYNC_ADDR`          | `:8080`             | Listen address                                |
| `MIHON_SYNC_DB`            | `./mihon-sync.db`   | SQLite database path                          |
| `MIHON_SYNC_RETENTION_DAYS`| `30`                | Tombstone retention before GC                 |
| `MIHON_SYNC_API_KEY`       | —                   | Bootstrap key; account ensured on serve start |

Put the server behind a reverse proxy with TLS (Caddy, nginx, Traefik…) if
it is reachable from the internet — API keys are bearer credentials.

## Key management

```sh
mihon-sync genkey -label "phone + tablet"   # create (prints key once)
mihon-sync listkeys                         # list key hashes/labels
mihon-sync revokekey mhk_...                # delete account + all its data
```

When running in Docker: `docker compose exec mihon-sync /mihon-sync genkey`.

## HTTP API

All endpoints except `/healthz` require `Authorization: Bearer <key>`.

| Endpoint                        | Description                                    |
| ------------------------------- | ---------------------------------------------- |
| `GET /healthz`                  | Liveness probe                                 |
| `GET /api/v1/auth/check`        | Validate the API key                           |
| `POST /api/v1/sync/push`        | Push a batch of changes; returns new revision  |
| `GET /api/v1/sync/pull?since=N` | Changes with revision > N (incl. tombstones)   |
| `GET /api/v1/sync/status`       | Revision + entity counts for the account       |

### Push example

```sh
curl -X POST -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  http://localhost:8080/api/v1/sync/push -d '{
    "device_id": "phone-uuid",
    "changes": {
      "mangas": [{"source_id": 7, "url": "/manga/one", "title": "One",
                  "favorite": true, "client_version": 5}],
      "chapters": [{"manga_source_id": 7, "manga_url": "/manga/one",
                    "url": "/c/1", "read": true, "last_page_read": 12,
                    "client_version": 3}],
      "categories": [{"name": "Reading", "order": 1}],
      "manga_categories": [{"manga_source_id": 7, "manga_url": "/manga/one",
                            "category": "Reading"}],
      "history": [{"manga_source_id": 7, "manga_url": "/manga/one",
                   "chapter_url": "/c/1", "last_read": 1717000000,
                   "read_duration": 300000}],
      "preferences": [{"key": "theme", "type": "string", "value": "dark"}]
    }
  }'
# → {"rev": 42}
```

### Pull example

```sh
curl -H "Authorization: Bearer $KEY" "http://localhost:8080/api/v1/sync/pull?since=41"
# → {"rev": 42, "changes": { ...only entities changed after revision 41... }}
```

Preference values are JSON with a `type` tag: `int`, `long`, `float`,
`string`, `boolean`, or `stringset` (array of strings).

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
