# places

A personal NYC places browser — Go API + embedded SvelteKit frontend, shipped as one static binary. Data comes from the [Obsidian NYC places wiki](https://obsidian.md); the `scripts/wiki-sync.py` tool keeps the two in sync.

## Architecture

```
places/
├── api/          Go 1.23 — chi router, modernc/sqlite (CGO_ENABLED=0)
├── web/          SvelteKit 5, adapter-static, prerender + ssr=false
├── scripts/      Operational tooling
│   └── wiki-sync.py   Upsert wiki → API (see below)
├── data/
│   └── places.db SQLite database (gitignored in prod, committed for dev seed)
└── Dockerfile    Distroless single-binary image
```

The Go binary embeds the SvelteKit build via `//go:embed`. One `docker run` serves everything.

Deployed on a home-lab Kubernetes cluster via ArgoCD; images built by an in-cluster buildkitd triggered from GitHub Actions.

## API

Live: **https://places.oleth.net**

Interactive docs: **https://places.oleth.net/api/docs** (Redoc, OpenAPI 3.1)

Spec: `GET /api/openapi.yaml` or `/api/openapi.json`

Key endpoints:

| Method | Path | Description |
|---|---|---|
| GET | `/api/places` | List places (search, tag, bbox, pagination) |
| POST | `/api/places` | Create a place |
| GET | `/api/places/:id` | Get a single place |
| PUT | `/api/places/:id` | Update a place |
| GET | `/api/tags` | Tag counts |
| GET | `/api/trips` | List trips |

Query params for `/api/places`:
- `q` — full-text search (name, description, neighborhood)
- `tag` — filter by tag (repeat for AND semantics: `?tag=bar&tag=brooklyn`)
- `sw_lat`, `sw_lng`, `ne_lat`, `ne_lng` — bounding box filter
- `page`, `per_page`

## Local development

```bash
# Terminal 1 — API on :8080
make dev-api

# Terminal 2 — SvelteKit dev server on :5173
make dev-web
```

## Build

```bash
make build      # build-web + build-api → ./places binary
make docker     # build container image
```

## Data — wiki sync

The source of truth is the Obsidian wiki at `~/obsidian/LLM-Wiki/nyc/wiki/places/`. The sync script reads every `*.md` file, parses YAML frontmatter + prose, and upserts to the API:

```bash
# Sync to live API
python3 scripts/wiki-sync.py --api-url https://places.oleth.net

# Dry run (no writes)
python3 scripts/wiki-sync.py --api-url https://places.oleth.net --dry-run

# Sync to local dev
make sync
```

A cron job (`places-wiki-sync`, Monday 10am ET) runs this automatically each week.

**What syncs:** name, description (prose + notes bullets), coordinates, tags, neighborhood, city, visited flag, source URL.

**Matching:** existing places are identified by slug (derived from the wiki filename). New slugs create; existing slugs update.

## Testing

```bash
make test    # Go tests
make check   # go vet + svelte-check
```

## Android client

See [`places-android`](https://github.com/jablated/places-android) — Jetpack Compose app with Map (MapLibre/OSM), List, Near Me, and Place Detail screens. No API key required.
