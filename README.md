# hacker tracker

A personal record of your work — the evidence you need for a review, a
promotion case, or a PIP you're climbing out of. Single user, self-hosted,
never an enterprise tool.

## Quick start

```sh
npm run setup     # node deps + go mod tidy
npm run db:up     # Postgres 17 in Docker
npm run dev       # Vite on :5173, Go on :8080
```

Open http://localhost:5173. Migrations run automatically when the server
starts.

## Getting work in

Four ways in, one write path. Everything lands in `raw_records`, normalises to
an `event`, and is curated by hand from there.

**Type it.** The capture form covers the work no API knows about — mentoring,
design review, the decision you talked someone out of. That work is the least
automatable and usually the most valuable in a review.

**Post to an ingest endpoint.** Mint one:

```sh
curl -s -X POST localhost:8080/api/source-accounts \
  -H 'content-type: application/json' \
  -d '{"label":"zapier"}'
```

The token comes back exactly once — only its hash is stored. Then anything that
can make an HTTP request can feed the system:

```sh
curl -X POST localhost:8080/api/ingest/$TOKEN \
  -H 'content-type: application/json' \
  -d '{"external_id":"gh-pr-1234","kind":"pr_merged",
       "title":"Migrate auth service","occurred_at":"2026-03-14T10:00:00Z",
       "url":"https://github.com/acme/api/pull/1234"}'
```

Send a single object or an array of up to 500. `external_id` must be stable
across redeliveries — webhook delivery is at-least-once, and a stable ID is
what stops a retry becoming a duplicate accomplishment.

Note that inbound third-party webhooks need a publicly reachable URL, which a
laptop doesn't have. Until this is hosted, the same endpoint still works for
local scripts posting to `localhost`.

**Pull connectors and file import** are not built yet. See `CLAUDE.md` for
where they slot in.

## Commands

| Command | What it does |
|---|---|
| `npm run setup` | Install node deps and resolve Go modules |
| `npm run db:up` / `db:down` | Start/stop Postgres |
| `npm run db:reset` | Drop the volume and start clean |
| `npm run db:psql` | Open psql against the dev database |
| `npm run dev` | Vite + Go together |
| `npm run build` | Production build to a single binary |
| `npm run typecheck` / `npm run vet` | Frontend types / Go vet |
