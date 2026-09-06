# hacker tracker

A personal record of your work — the evidence you need for a review, a
promotion case, or a PIP you're climbing out of. Single user, self-hosted,
never an enterprise tool.

## Quick start

```sh
cp .env.example .env              # non-secret local config
cp .env.local.example .env.local  # secrets, git-ignored
npm run setup                     # node deps + go mod tidy
npm run db:up                     # Postgres 17 in Docker
npm run dev                       # Vite on :5173, Go on :8080
```

Open http://localhost:5173. Migrations run automatically when the server
starts.

Postgres is published on **5442**, not 5432, so it can't collide with a
Homebrew or Postgres.app install you already have. `npm run db:up` waits for
the container to report healthy and fails loudly if it can't start.

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

**Connect GitHub.** Put the token in `.env.local`, restart the server, then
connect it from the Sources panel by naming the variable rather than pasting the
token:

```sh
# .env.local
GITHUB_TOKEN=github_pat_...
```

Then enter `GITHUB_TOKEN` — just the name — in the Sources panel. Only the name
is stored (`env:GITHUB_TOKEN`); the token never touches the database. A value
that looks like a credential is refused in the browser before it is ever sent,
and again on the server before it can be echoed back in an error.

A fine-grained token needs no permissions at all for public repositories. For
private ones, select those repos on the token and grant **Pull requests: Read**. Connecting verifies the token immediately and echoes back the login it
belongs to — a token that authenticates but can see nothing is GitHub's nastiest
failure, because it produces syncs that succeed and return zero events.

Syncs run on demand ("Sync now") and once at server startup. There is no
background scheduler: the first sync backfills a year, and later ones only cover
the time since the last success plus a day of overlap. A sync reports what it
*examined*, not just what it found, so an empty result is legible rather than
silently reassuring.

Set `GITHUB_API_BASE_URL` for GitHub Enterprise Server.

**File import** is not built yet. See `CLAUDE.md` for where it slots in.

## Configuration

Two files, both git-ignored:

| File | Holds | Read by |
|---|---|---|
| `.env` | Non-secret local config (ports, database name) | Compose natively, and the server |
| `.env.local` | Secrets (`GITHUB_TOKEN`) | The server; passed to the container by Compose |

The server loads `.env.local` first, then `.env`, and **never overwrites a
variable that is already set** — so a shell export or a CI secret still wins
without editing either file.

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
| `npm run test:go` | Go tests |
