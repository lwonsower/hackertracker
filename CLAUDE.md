# CLAUDE.md

Guidance for Claude Code (claude.ai/code) when working in this repository.

## Project state

A work-tracking tool: capture evidence of what you did, curate it into projects,
export a review packet. **Multi-account** — every person gets their own account,
with no sharing between them. Not an enterprise product: no teams, no org
admin, no shared workspaces (yet).

Built so far: the schema, the ingest pipeline, the generic HTTP ingest endpoint,
manual capture, a timeline, and the GitHub pull connector with on-demand and
startup syncs. Not built yet: projects/entries UI, the review packet export, CSV
import. The tables for the curation layer exist and are unused.

Go tests cover the connector, the deterministic event IDs and credential
resolution (`npm run test:go`). The frontend has no tests.

## Commands

Run from the repo root unless noted:

- `npm run setup` — node deps for root and frontend, plus `go mod tidy`. Run once.
- `npm run db:up` / `npm run db:down` — Postgres 17 via Compose, published on **5442** to avoid colliding with a local Postgres install. `db:up` waits for a healthy container. `db:reset` drops the volume; `db:psql` opens a shell; `db:logs` tails it.
- `npm run dev` — Vite (`:5173`) and the Go server (`:8080`) together. The normal way to work.
- `npm run dev:go` — Go server only.
- `npm run build` — Vite build then `go build`, producing `backend/backend`.
- `npm run typecheck` — `tsc --noEmit`. `npm run vet` — `go vet ./...`. `npm run test:go` — Go tests.
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
- `internal/auth` — Google sign-in and sessions.
- `internal/dotenv` — loads `.env.local` then `.env` at startup, searching up
  from the working directory so `go run .` from `backend/` finds the repo-root
  files. Never overwrites an existing environment variable.
- `internal/secrets` — resolves `credentials_ref` pointers (`env:NAME`) into
  actual credentials. The database never stores a secret.
- `internal/syncer` — drives pull connectors: resolve credentials, loop the
  Fetcher over its cursor, feed the pipeline, record sync state.
- `internal/connectors/github` — the first pull connector.

**`frontend/`** — Vite + React + TypeScript, `react-router-dom`.

- `layout/AppShell.tsx` — the sidebar and nav, rendering pages through an
  `<Outlet />`. Every route is nested under it in `main.tsx`.
- `routes/Timeline.tsx` (`/`) — capture form plus the event timeline.
- `routes/Sources.tsx` (`/sources`) — connect a source, see its sync state, run
  a sync.
- `routes/Goals.tsx` (`/goals`) — placeholder.
- `api.ts` — the API client. Styling is split between `styles/tokens.css`
  (design tokens — reference these, don't hard-code values) and
  `styles/global.css`.

Deep links survive a hard refresh because the Go SPA handler falls back to
`index.html` for any path outside `/api`.

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

## The GitHub connector

Shaped almost entirely by three documented search API limits: 30 requests/minute
authenticated, 100 results per page, and a hard cap of **1,000 results per
query**. The cap is why a backfill issues one query per calendar month rather
than one for the whole range.

- Two streams, run one after the other, each chronologically:
  `is:pr author:X is:merged merged:A..B` and
  `is:pr reviewed-by:X -author:X updated:A..B`. Excluding your own PRs from the
  review stream avoids double-counting.
- **Windows are calendar years that subdivide into months only when needed.**
  Fixed month windows were sized for the worst case and made a ten-year backfill
  240 paced queries (~8 minutes). Since the API returns `total_count` on the
  first page, density is measured rather than assumed: a year over the 1,000-result
  cap is discarded and re-fetched month by month, costing one wasted probe; a
  sparse year costs one query. A decade of sparse history is 22 queries, not 264.
- A month still over the cap cannot be subdivided further, so it increments
  `windows_truncated` and the report says records were lost.
- External IDs are prefixed per stream (`pr:acme/api#12`, `review:acme/api#12`)
  so the same PR in both streams cannot collide in `raw_records`, while both
  carry `subject_key: github:acme/api#12`.
- `Sync` covers a bounded number of windows and returns a cursor; the runner
  calls it in a loop and checkpoints the cursor between rounds, so an
  interrupted backfill resumes rather than restarting.
- **Stats accumulate across rounds, they do not reset per call.** A backfill is
  several Sync calls; resetting would make a 26-query run report whatever the
  last round issued. There is a test pinning this.
- Review timestamps are approximate — the search API returns pull requests, not
  review events, so there is no review timestamp available. Events carry
  `timestamp_precision: "approximate"` in their payload so a later upgrade to
  real timestamps can find exactly which events to re-normalise.
- `merged_at` is not guaranteed in search results; normalisation falls back to
  `closed_at`, then `updated_at`.

## Sync behaviour

- **No scheduler, and no sync at startup any more.** Syncs run on demand only.
  The global `SyncAll` was removed when accounts arrived: syncing every account's
  sources on boot is both unbounded work and a scoping violation. Scheduled
  syncing needs a job queue that does not exist yet. `SyncAccount` covers one
  account's sources.
- **The first sync reaches back ten years** (`Runner.BackfillYears`, overridable
  with `SYNC_BACKFILL_YEARS`). One year was the original default and was wrong:
  this tool reconstructs a career's evidence, and someone whose most recent
  merged PR is two years old got a successful sync with zero events. Year-sized
  windows are what make the wider default affordable.
- **`POST /api/source-accounts/{id}/sync` accepts `{"since": "YYYY-MM-DD"}`**,
  which beats both the watermark and the default. That is the only way to reach
  history older than a successful sync, since success advances `last_synced_at`.
- **Overlap is deliberate.** Sync from `last_synced_at - 24h`. Search indexes are
  eventually consistent and clocks drift, so an exact boundary loses records
  silently. Re-ingesting costs nothing, which is what makes the slack affordable.
- **`last_synced_at` advances only on a complete run**, and is coalesced on save
  so a failure records its error without erasing the last success.
- **Reports say what was examined**, not just what was produced. "Found nothing"
  and "never issued a query" must not look alike — the signature GitHub failure
  is a token that authenticates but sees nothing.
- **The empty-result note names the search window first.** An earlier version led
  with scopes and SSO and sent someone hunting a permissions problem when their
  most recent merged PR was simply older than the range. Cheapest cause first.

## Accounts and isolation

**Data belongs to an account, not a user.** Today every account has exactly one
user, but the indirection means shared workspaces or a manager view later need
no rewrite of every foreign key.

**Isolation is enforced by the database, not by discipline.** Every content
table has `account_id` and a row-level security policy filtering on
`current_setting('app.account_id')`. A forgotten `WHERE` clause returns nothing
instead of everyone's data.

Three things make that real, and all three are load-bearing:

1. **`FORCE ROW LEVEL SECURITY`**, or the table owner ignores its own policies.
2. **`SET LOCAL ROLE hackertracker_app`** in `store.DB.Scope`. This is the
   subtle one: *superusers ignore RLS entirely*, and the role Compose creates
   from `POSTGRES_USER` is a superuser. Without the role drop the policies are
   enabled, forced, and completely inert. The role has no LOGIN; it exists only
   to shed privileges.
3. **`db.VerifyIsolation` at startup**, which refuses to serve if the drop does
   not take effect or if any events are visible with no account set. The failure
   it guards against is silent, so it must be fatal.

`store.Store` cannot be constructed outside `DB.Scope`, so a handler has no way
to reach data without naming an account. Both settings are transaction-local and
unwind on commit.

**Unscoped queries live only in `store/identity.go`** — sessions, users and
ingest-token lookups, the queries that *establish* a scope and so cannot run
inside one. Keep that file small; it is the one place the database will not
catch a mistake.

Inside a scope, "belongs to someone else" and "does not exist" are the same
answer: a 404, never a 403, so an id cannot be probed for existence.

## Sign-in

Google authorization-code flow with PKCE. The ID token signature is
deliberately not verified: the code is exchanged directly with Google over TLS
and the profile read from the userinfo endpoint over the same channel, so there
is no untrusted intermediary — which removes a JWKS dependency and a class of
verification bugs. Unverified email addresses are rejected, because account
linking matches on email.

Sessions are opaque random tokens stored server-side (hashed), not JWTs:
revocable immediately, nothing sensitive in the cookie. HttpOnly, SameSite=Lax,
Secure when hosted.

`user_identities` is separate from `users` so a second provider links to an
existing person instead of silently creating a duplicate account holding none of
their history.

`redirect_to` is restricted to same-site paths, so the handshake cannot be used
as an open redirect. `auth_states` rows are deleted on consumption, making every
sign-in link single use.

The sign-in button follows Google's published branding spec (dark variant:
`#131314` fill, `#8E918F` stroke, 12px before the mark and 10px after, 14/20
type). Those values are hard-coded rather than tokenised on purpose — they are
Google's, and must not move if the palette is re-themed. The G mark is
referenced from Google rather than redrawn, because their guidelines require the
standard-colour asset unaltered; self-host it in `frontend/public/` to drop the
third-party request.

## Deployment modes

`DEPLOYMENT_MODE` is `self-host` (default) or `hosted`. Self-host enables three
things that each assume a single trusted user, and hosted disables all three:

- **Orphan account adoption** — the first sign-in claims an account that has data
  but no users. This is how rows migrated from the single-user era find an owner.
  Hosted, it would hand a stranger someone else's data.
- **`env:` credential references** — with several accounts, two both referencing
  `env:GITHUB_TOKEN` would share one token, and each would see the other's
  GitHub data captured under their own name.
- **`DEV_SIGN_IN_EMAIL`** — bypasses Google entirely.

## Configuration and secrets

- `.env` holds non-secret local config and is read natively by Compose;
  `.env.local` holds secrets. Both are git-ignored, with `.env.example` and
  `.env.local.example` committed.
- **The environment always wins over the files**, and `.env.local` wins over
  `.env`. That ordering is what lets CI or a one-off export override without
  anyone editing a file.
- `internal/dotenv` is hand-written rather than a library because a clever
  parser's failure mode is bad here: a value silently mangled by quote or
  comment handling surfaces much later as an unexplained 401. Inline `#`
  comments are deliberately *not* stripped from unquoted values, and surrounding
  whitespace *is* trimmed — a stray trailing space in a pasted token is a common
  and very confusing mistake.
- **A pasted credential is rejected at two layers.** The browser checks before
  submitting so the value never leaves the page; `secrets.Resolve` checks again
  before the value is used or echoed. The old error quoted the name back, which
  is how a real token once ended up in an HTTP response. `ErrPastedCredential`
  deliberately says nothing about what it saw; ordinary typos still name the
  variable, because that is what makes the error useful.

## Conventions

- Register API handlers under `/api/` on the mux in `main.go` so the SPA
  fallback exclusion covers them.
- SQL belongs in `internal/store`. Nothing else imports pgx.
- Return `store.ErrNotFound` rather than leaking `pgx.ErrNoRows` to callers.
- User-fixable input problems are `core.ValidationError`, which the HTTP layer
  turns into a 400; everything else is a 500 with the detail logged, not
  returned.
- Credentials are referenced, never stored: `credentials_ref` holds `env:NAME`.
  Nothing should ever write a token into Postgres.
- A new pull connector implements `core.Fetcher` plus `core.Normalizer`,
  registers its normaliser on the pipeline and a `BuildFunc` on the runner. Keep
  the normaliser stateless and separate so `raw_records` can be re-normalised
  without credentials.
