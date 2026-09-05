# CLAUDE.md

Guidance for Claude Code (claude.ai/code) when working in this repository.

## Project state

A personal work-tracking tool: capture evidence of what you did, curate it into
projects, export a review packet. Single user, self-hosted, explicitly not an
enterprise product.

Built so far: the schema, the ingest pipeline, the generic HTTP ingest endpoint,
manual capture and a timeline. Not built yet: projects/entries UI, the review
packet export, the GitHub connector, CSV import. The tables for the curation
layer exist and are unused.

There is no test suite yet in either half.

## Commands

Run from the repo root unless noted:

- `npm run setup` — node deps for root and frontend, plus `go mod tidy`. Run once.
- `npm run db:up` / `npm run db:down` — Postgres 17 via Compose. `db:reset` drops the volume; `db:psql` opens a shell.
- `npm run dev` — Vite (`:5173`) and the Go server (`:8080`) together. The normal way to work.
- `npm run dev:go` — Go server only.
- `npm run build` — Vite build then `go build`, producing `backend/backend`.
- `npm run typecheck` — `tsc --noEmit`. `npm run vet` — `go vet ./...`.
- `docker compose --profile app up --build` — whole stack in containers.

No lint or test command is configured.

## Architecture

A monorepo whose two halves combine into one binary.

**`backend/`** — Go, `net/http` only, no framework.

- `main.go` — wiring: migrate, connect, register routes, serve. Also the SPA
  handler, which serves the embedded frontend and falls back to `index.html`
  for unknown paths so client-side routing survives a hard refresh. Paths under
  `/api/` are excluded from that fallback: a typo'd endpoint should 404 rather
  than return HTML the client fails to parse as JSON.
- `internal/core` — domain types. `Event`, `RawRecord`, the strict `Envelope`,
  and the `Normalizer` / `Fetcher` interfaces.
- `internal/store` — the only package that knows SQL. Hand-written pgx queries.
- `internal/ingest` — the single write path (`Pipeline.Ingest`).
- `internal/db` — pool plus embedded goose migrations.
- `internal/httpapi` — HTTP handlers.

**`frontend/`** — Vite + React + TypeScript, `react-router-dom`. Routes in
`src/routes/`, API client in `src/api.ts`, styling split between
`styles/tokens.css` (design tokens — reference these, don't hard-code values)
and `styles/global.css`.

**The build seam**: `frontend/vite.config.ts` sets `build.outDir` to
`../backend/web`, which is what `//go:embed web` picks up, so `go build` after a
Vite build embeds the real production output. In dev this doesn't apply: Vite
serves the app and proxies `/api` and `/healthz` to `:8080`, so the browser sees
one origin and CORS never arises.

## The ideas that shape the code

**One write path, four producers.** Pull connectors, webhook deliveries, file
imports and the capture form all build `RawRecord`s and call
`Pipeline.Ingest`. They differ only in how records are produced. Manual entry is
not special — it is a `source_account` with `mode='manual'`.

**Acquisition and normalisation are separate jobs.** `Fetcher` (optional, pull
only) gets bytes out of a system; `Normalizer` (required) turns bytes into
events. This is why push and pull are not architecturally different downstream,
and why a new integration is usually one file.

**Raw payloads are kept forever.** You can re-normalise later; you cannot
re-fetch after the token dies — which is exactly the situation this tool exists
for. `raw_records` is append-only, deduplicated by content hash.

**The capture layer is machine-owned, the curation layer is human-owned.**
Sync writes `raw_records` and `events` and never touches `annotations`,
`projects`, `project_entries` or the tag tables.

**Re-normalisation is an upsert pass, never a rebuild.** The curation tables
cascade from `events`, so `delete from events` destroys annotations and tags.
To re-run a fixed normaliser, upsert over `raw_records`; do not truncate.

**Project narrative is an append-only log, not columns.** `project_entries`
rows carry a `kind` (`hypothesis`, `update`, `risk`, `outcome`, `retro`) and a
date. The sequence is the evidence — predicted in January, shipped in March —
which a single editable summary column cannot express.

**The ingest endpoint is strict.** It accepts our envelope, not arbitrary JSON.
Mapping expressions are a deliberate v2, to be built against real payloads
collected in `ingest_log` rather than designed blind. `external_id` is required
because delivery is at-least-once.

## Conventions

- Register API handlers under `/api/` on the mux in `main.go` so the SPA
  fallback exclusion covers them.
- SQL belongs in `internal/store`. Nothing else imports pgx.
- Return `store.ErrNotFound` rather than leaking `pgx.ErrNoRows` to callers.
- User-fixable input problems are `core.ValidationError`, which the HTTP layer
  turns into a 400; everything else is a 500 with the detail logged, not
  returned.
