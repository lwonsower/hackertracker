-- +goose Up

-- ── arcs hold arcs ───────────────────────────────────────────────────────
--
-- The goals tier is gone. What it did well was separate "a standard you are
-- measured against" from "a line of work", and the useful half of that was
-- never the goal itself — it was the criterion: a named thing that can be
-- empty, so you can see nothing supports it yet.
--
-- Nesting recovers exactly that with one concept instead of three. "Promotion
-- to Staff" is an arc holding "Cross-team influence", which holds events. A
-- criterion with no evidence becomes an arc with nothing in it, and an arc can
-- finally support another arc — which the flat model could not express at all.
alter table arcs add column parent_id uuid references arcs on delete set null;

-- ON DELETE SET NULL, not CASCADE: deleting a parent must never take a child's
-- narrative and evidence with it. An orphaned child becomes top-level.
create index arcs_parent_idx on arcs (parent_id) where parent_id is not null;

-- The one cycle SQL can catch on its own. Longer loops are refused in the
-- store, which walks the ancestors before saving.
alter table arcs add constraint arcs_parent_not_self check (parent_id is null or parent_id <> id);

-- ── the goals tier ───────────────────────────────────────────────────────
drop table if exists goal_entries;
drop table if exists criterion_evidence;
drop table if exists goal_criteria;
drop table if exists goals;

-- +goose Down

create table goals (
    id         uuid primary key default gen_random_uuid(),
    account_id uuid not null references accounts on delete cascade,
    title      text not null,
    summary    text,
    target_at  date,
    status     text not null default 'open'
               check (status in ('open', 'met', 'not_met', 'dropped')),
    created_at timestamptz not null default now()
);

create table goal_criteria (
    id          uuid primary key default gen_random_uuid(),
    account_id  uuid not null references accounts on delete cascade,
    goal_id     uuid not null references goals on delete cascade,
    title       text not null,
    description text,
    position    integer not null default 0
);

create table criterion_evidence (
    id           uuid primary key default gen_random_uuid(),
    account_id   uuid not null references accounts on delete cascade,
    criterion_id uuid not null references goal_criteria on delete cascade,
    arc_id       uuid references arcs on delete cascade,
    event_id     uuid references events on delete cascade,
    created_at   timestamptz not null default now(),
    check (num_nonnulls(arc_id, event_id) = 1)
);

create unique index criterion_evidence_arc_key
    on criterion_evidence (criterion_id, arc_id) where arc_id is not null;
create unique index criterion_evidence_event_key
    on criterion_evidence (criterion_id, event_id) where event_id is not null;

create table goal_entries (
    id          uuid primary key default gen_random_uuid(),
    account_id  uuid not null references accounts on delete cascade,
    goal_id     uuid not null references goals on delete cascade,
    kind        text check (kind is null or kind in
                ('feedback', 'check_in', 'scope_change', 'reflection')),
    body        text not null,
    occurred_at timestamptz not null default now(),
    created_at  timestamptz not null default now()
);

create index goals_account_idx              on goals (account_id);
create index goal_criteria_account_idx      on goal_criteria (account_id);
create index goal_criteria_goal_idx         on goal_criteria (goal_id, position);
create index criterion_evidence_account_idx on criterion_evidence (account_id);
create index criterion_evidence_criterion_idx on criterion_evidence (criterion_id);
create index goal_entries_account_idx       on goal_entries (account_id);
create index goal_entries_goal_idx          on goal_entries (goal_id, occurred_at);

-- +goose StatementBegin
do $$
declare
    owned text;
begin
    foreach owned in array array[
        'goals', 'goal_criteria', 'criterion_evidence', 'goal_entries'
    ] loop
        execute format('alter table %I enable row level security', owned);
        execute format('alter table %I force row level security', owned);
        execute format($p$
            create policy account_isolation on %I
            using (account_id = nullif(current_setting('app.account_id', true), '')::uuid)
            with check (account_id = nullif(current_setting('app.account_id', true), '')::uuid)
        $p$, owned);
        execute format('grant select, insert, update, delete on %I to hackertracker_app', owned);
    end loop;
end $$;
-- +goose StatementEnd

alter table arcs drop constraint arcs_parent_not_self;
drop index if exists arcs_parent_idx;
alter table arcs drop column parent_id;
