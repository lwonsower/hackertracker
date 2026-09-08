-- +goose Up

-- ── projects become arcs ─────────────────────────────────────────────────
alter table projects        rename to arcs;
alter table project_entries rename to arc_entries;
alter table project_events  rename to arc_events;
alter table project_tags    rename to arc_tags;

alter table arc_entries rename column project_id to arc_id;
alter table arc_events  rename column project_id to arc_id;
alter table arc_tags    rename column project_id to arc_id;

alter index project_entries_project_idx rename to arc_entries_arc_idx;
alter index project_events_event_idx    rename to arc_events_event_idx;
alter index projects_account_idx        rename to arcs_account_idx;
alter index project_entries_account_idx rename to arc_entries_account_idx;
alter index project_events_account_idx  rename to arc_events_account_idx;
alter index project_tags_account_idx    rename to arc_tags_account_idx;

-- ── arc status ───────────────────────────────────────────────────────────
-- Continuity is derived from target_at, so 'ongoing' is gone.
alter table arcs drop constraint projects_status_check;

update arcs set status = case status
    when 'shipped'   then 'done'
    when 'abandoned' then 'dropped'
    else 'open'
end;

alter table arcs alter column status set default 'open';
alter table arcs add constraint arcs_status_check
    check (status in ('open', 'done', 'dropped'));

-- ── entry kinds become optional labels ───────────────────────────────────
alter table arc_entries drop constraint project_entries_kind_check;
alter table arc_entries alter column kind drop not null;
alter table arc_entries add constraint arc_entries_kind_check
    check (kind is null or kind in ('hypothesis', 'update', 'risk', 'outcome', 'retro'));

-- ── soft-deleted events ──────────────────────────────────────────────────
alter table events add column deleted_at timestamptz;

create index events_live_occurred_idx on events (occurred_at desc) where deleted_at is null;

-- security_invoker makes the view respect the caller's row-level security.
-- Without it the view runs as its owner, which is a superuser, and every
-- account's events become visible through it.
create view live_events with (security_invoker = true) as
    select * from events where deleted_at is null;

grant select on live_events to hackertracker_app;

-- ── goals ────────────────────────────────────────────────────────────────
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

-- Evidence attaches to a criterion, never to a goal directly.
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

-- +goose Down
drop table if exists goal_entries;
drop table if exists criterion_evidence;
drop table if exists goal_criteria;
drop table if exists goals;

drop view if exists live_events;
drop index if exists events_live_occurred_idx;
alter table events drop column if exists deleted_at;

alter table arc_entries drop constraint arc_entries_kind_check;
alter table arc_entries alter column kind set not null;
alter table arc_entries add constraint project_entries_kind_check
    check (kind in ('hypothesis', 'update', 'risk', 'outcome', 'retro'));

alter table arcs drop constraint arcs_status_check;
alter table arcs alter column status set default 'active';
alter table arcs add constraint projects_status_check
    check (status in ('active', 'shipped', 'abandoned', 'ongoing'));

alter index arc_entries_arc_idx     rename to project_entries_project_idx;
alter index arc_events_event_idx    rename to project_events_event_idx;
alter index arcs_account_idx        rename to projects_account_idx;
alter index arc_entries_account_idx rename to project_entries_account_idx;
alter index arc_events_account_idx  rename to project_events_account_idx;
alter index arc_tags_account_idx    rename to project_tags_account_idx;

alter table arc_entries rename column arc_id to project_id;
alter table arc_events  rename column arc_id to project_id;
alter table arc_tags    rename column arc_id to project_id;

alter table arcs        rename to projects;
alter table arc_entries rename to project_entries;
alter table arc_events  rename to project_events;
alter table arc_tags    rename to project_tags;
