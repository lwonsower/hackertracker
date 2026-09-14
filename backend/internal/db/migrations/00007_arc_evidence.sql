-- +goose Up

-- ── an arc can be evidence for several arcs ──────────────────────────────
--
-- parent_id gave every arc exactly one parent, which made filing exclusive:
-- putting the auth migration under "technical leadership" stopped it being a
-- line of work in its own right, and it could not also support the promotion
-- case. Evidence is not exclusive. The same piece of work legitimately
-- supports several things at once, which is most of the point of keeping it.
--
-- So arcs relate the way events already do: a link table, many to many. Filing
-- an arc into another is now the same act as filing an event into it.
create table arc_arcs (
    account_id  uuid not null references accounts on delete cascade,
    -- The arc doing the holding.
    arc_id      uuid not null references arcs on delete cascade,
    -- The arc filed into it as evidence.
    evidence_id uuid not null references arcs on delete cascade,
    created_at  timestamptz not null default now(),
    primary key (arc_id, evidence_id),
    check (arc_id <> evidence_id)
);

create index arc_arcs_evidence_idx on arc_arcs (evidence_id);
create index arc_arcs_account_idx  on arc_arcs (account_id);

-- Carry the tree over: a parent becomes a link, and nothing is lost.
insert into arc_arcs (account_id, arc_id, evidence_id)
select account_id, parent_id, id from arcs where parent_id is not null;

alter table arcs drop constraint arcs_parent_not_self;
drop index if exists arcs_parent_idx;
alter table arcs drop column parent_id;

alter table arc_arcs enable row level security;
alter table arc_arcs force row level security;
create policy account_isolation on arc_arcs
    using (account_id = nullif(current_setting('app.account_id', true), '')::uuid)
    with check (account_id = nullif(current_setting('app.account_id', true), '')::uuid);
grant select, insert, update, delete on arc_arcs to hackertracker_app;

-- +goose Down

alter table arcs add column parent_id uuid references arcs on delete set null;
create index arcs_parent_idx on arcs (parent_id) where parent_id is not null;

-- Only one parent survives the trip back; the earliest link wins.
update arcs a set parent_id = (
    select l.arc_id from arc_arcs l
    where l.evidence_id = a.id
    order by l.created_at, l.arc_id
    limit 1
);

-- A restored tree cannot contain the cycles the link graph allowed.
update arcs set parent_id = null where parent_id = id;

alter table arcs add constraint arcs_parent_not_self check (parent_id is null or parent_id <> id);

drop table if exists arc_arcs;
