-- +goose Up

-- ── where data comes from ────────────────────────────────────────────────
-- One row per connected thing: a GitHub account, a webhook endpoint, the
-- manual-entry pseudo-source. `mode` is what distinguishes how records
-- arrive; everything downstream of raw_records is identical regardless.
create table source_accounts (
    id                  uuid primary key default gen_random_uuid(),
    source              text not null,
    mode                text not null check (mode in ('pull', 'push', 'manual', 'import')),
    label               text not null,
    external_account_id text,
    credentials_ref     text,
    ingest_token_hash   text unique,
    created_at          timestamptz not null default now(),
    unique (source, label)
);

create table sync_state (
    source_account_id uuid primary key references source_accounts on delete cascade,
    cursor            text,
    last_synced_at    timestamptz,
    last_error        text
);

-- Every inbound request, accepted or not. raw_records only holds successes,
-- but the question you actually ask at 11pm is "why didn't my Zapier thing
-- show up" — which is unanswerable without the failures.
create table ingest_log (
    id                bigserial primary key,
    source_account_id uuid references source_accounts on delete set null,
    received_at       timestamptz not null default now(),
    body              jsonb,
    status            text not null check (status in ('accepted', 'rejected', 'error')),
    error             text
);

create index ingest_log_received_at_idx on ingest_log (received_at desc);

-- ── capture layer: machine-owned, safe to truncate and rebuild ───────────
create table raw_records (
    id                bigserial primary key,
    source_account_id uuid not null references source_accounts on delete cascade,
    external_id       text not null,
    payload           jsonb not null,
    content_hash      text not null,
    fetched_at        timestamptz not null default now(),
    unique (source_account_id, external_id, content_hash)
);

-- events.id is NOT generated here: it is a deterministic UUIDv5 derived from
-- (source_account_id, external_id, kind) in Go, so an event's ID is computable
-- without a round-trip and a rebuild into a fresh database reproduces it.
--
-- Idempotent redelivery comes from the unique index below plus ON CONFLICT,
-- not from the ID scheme. And note the curation tables cascade from here:
-- deleting an event deletes its annotations and tags. Re-normalising means
-- upserting over raw_records, never truncating this table.
create table events (
    id                uuid primary key,
    source_account_id uuid not null references source_accounts on delete cascade,
    external_id       text not null,
    kind              text not null,
    subject_key       text,
    title             text not null,
    url               text,
    occurred_at       timestamptz not null,
    ingested_at       timestamptz not null default now(),
    payload           jsonb not null default '{}',
    unique (source_account_id, external_id, kind)
);

create index events_occurred_at_idx on events (occurred_at desc);
create index events_subject_key_idx on events (subject_key) where subject_key is not null;

-- ── curation layer: human-owned, never written by sync ───────────────────
create table annotations (
    event_id   uuid primary key references events on delete cascade,
    impact     text,
    note       text,
    starred    boolean not null default false,
    updated_at timestamptz not null default now()
);

create index annotations_starred_idx on annotations (starred) where starred;

create table projects (
    id         uuid primary key default gen_random_uuid(),
    title      text not null,
    status     text not null default 'active'
               check (status in ('active', 'shipped', 'abandoned', 'ongoing')),
    summary    text,
    started_at date,
    target_at  date,
    ended_at   date,
    created_at timestamptz not null default now()
);

-- Append-only dated narrative rather than hypothesis/outcome columns: the
-- sequence is the evidence. Predicted in January, shipped in March, outcome
-- in April reads as calibrated judgement; a column you can silently revise
-- reads as nothing.
create table project_entries (
    id          uuid primary key default gen_random_uuid(),
    project_id  uuid not null references projects on delete cascade,
    kind        text not null
                check (kind in ('hypothesis', 'update', 'risk', 'outcome', 'retro')),
    body        text not null,
    occurred_at timestamptz not null default now(),
    created_at  timestamptz not null default now()
);

create index project_entries_project_idx on project_entries (project_id, occurred_at);

create table project_events (
    project_id uuid references projects on delete cascade,
    event_id   uuid references events on delete cascade,
    primary key (project_id, event_id)
);

create index project_events_event_idx on project_events (event_id);

create table tags (
    id   uuid primary key default gen_random_uuid(),
    name text not null unique
);

create table event_tags (
    event_id uuid references events on delete cascade,
    tag_id   uuid references tags on delete cascade,
    primary key (event_id, tag_id)
);

create table project_tags (
    project_id uuid references projects on delete cascade,
    tag_id     uuid references tags on delete cascade,
    primary key (project_id, tag_id)
);

-- +goose Down
drop table if exists project_tags;
drop table if exists event_tags;
drop table if exists tags;
drop table if exists project_events;
drop table if exists project_entries;
drop table if exists projects;
drop table if exists annotations;
drop table if exists events;
drop table if exists raw_records;
drop table if exists ingest_log;
drop table if exists sync_state;
drop table if exists source_accounts;
