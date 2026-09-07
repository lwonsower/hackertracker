-- +goose Up

-- ── identity ─────────────────────────────────────────────────────────────
--
-- Data is owned by an ACCOUNT, not a user, even though today every account has
-- exactly one user. The indirection costs one join now and means shared
-- workspaces or a manager view later need no rewrite of every foreign key.
create table accounts (
    id         uuid primary key default gen_random_uuid(),
    name       text,
    created_at timestamptz not null default now()
);

create table users (
    id           uuid primary key default gen_random_uuid(),
    account_id   uuid not null references accounts on delete cascade,
    email        text not null,
    name         text,
    avatar_url   text,
    created_at   timestamptz not null default now(),
    last_seen_at timestamptz
);

create unique index users_email_key on users (lower(email));
create index users_account_idx on users (account_id);

-- One row per provider a user can sign in with. Separate from users so a
-- second provider can be linked to an existing person rather than creating a
-- duplicate account, and so no provider is baked into the user record.
create table user_identities (
    id         uuid primary key default gen_random_uuid(),
    user_id    uuid not null references users on delete cascade,
    provider   text not null,
    subject    text not null,
    email      text,
    created_at timestamptz not null default now(),
    unique (provider, subject)
);

create index user_identities_user_idx on user_identities (user_id);

-- Opaque server-side sessions rather than JWTs: revocable, and nothing
-- sensitive travels in the cookie. Only the hash is stored, so a database leak
-- does not hand over live sessions.
create table sessions (
    id           uuid primary key default gen_random_uuid(),
    user_id      uuid not null references users on delete cascade,
    token_hash   text not null unique,
    created_at   timestamptz not null default now(),
    expires_at   timestamptz not null,
    last_used_at timestamptz,
    user_agent   text
);

create index sessions_user_idx on sessions (user_id);
create index sessions_expires_idx on sessions (expires_at);

-- Short-lived OAuth handshake state: CSRF token, OIDC nonce and PKCE verifier.
create table auth_states (
    state       text primary key,
    nonce       text not null,
    verifier    text not null,
    redirect_to text,
    created_at  timestamptz not null default now(),
    expires_at  timestamptz not null
);

create index auth_states_expires_idx on auth_states (expires_at);

-- ── ownership ────────────────────────────────────────────────────────────
--
-- account_id is denormalised onto every owned table, including join tables.
-- Deriving ownership through source_accounts would work, but every row-level
-- security policy would then need a join, which is both slower and much easier
-- to get subtly wrong.

-- +goose StatementBegin
do $$
declare
    default_account uuid;
    owned text;
begin
    -- Adopt any pre-existing rows into one account rather than deleting them.
    -- The first person to sign in claims this account (see the self-host
    -- bootstrap in internal/auth), so existing history is not orphaned.
    if exists (select 1 from source_accounts) then
        insert into accounts (name) values ('Default') returning id into default_account;
    end if;

    foreach owned in array array[
        'source_accounts', 'sync_state', 'ingest_log', 'raw_records', 'events',
        'annotations', 'projects', 'project_entries', 'project_events',
        'tags', 'event_tags', 'project_tags'
    ] loop
        execute format('alter table %I add column account_id uuid references accounts on delete cascade', owned);
        if default_account is not null then
            execute format('update %I set account_id = %L', owned, default_account);
        end if;
    end loop;

    -- ingest_log keeps nullable ownership: a delivery to an unknown token has
    -- no account, and those rows are deliberately invisible to every user.
    foreach owned in array array[
        'source_accounts', 'sync_state', 'raw_records', 'events',
        'annotations', 'projects', 'project_entries', 'project_events',
        'tags', 'event_tags', 'project_tags'
    ] loop
        execute format('alter table %I alter column account_id set not null', owned);
        execute format('create index %I on %I (account_id)', owned || '_account_idx', owned);
    end loop;
end $$;
-- +goose StatementEnd

-- ── constraints that were global and must now be per-account ─────────────
alter table source_accounts drop constraint source_accounts_source_label_key;
alter table source_accounts add constraint source_accounts_account_source_label_key
    unique (account_id, source, label);

-- One person's "mentoring" is not another's.
alter table tags drop constraint tags_name_key;
alter table tags add constraint tags_account_name_key unique (account_id, name);

-- events and raw_records stay keyed on source_account_id, which is itself
-- account-scoped, so their uniqueness is already per-account.

-- ── a role that cannot bypass its own policies ───────────────────────────
--
-- Row-level security is bypassed unconditionally by superusers and by roles
-- with BYPASSRLS. The role Compose creates from POSTGRES_USER *is* a
-- superuser, so policies alone would be silently inert — enabled, forced, and
-- doing nothing.
--
-- So the application drops to this role for every data query (SET LOCAL ROLE in
-- store.Scope, reverted automatically at commit). It has no LOGIN, so it is not
-- a second credential to manage; it exists purely to shed the privileges that
-- would defeat the policies. internal/db verifies at startup that the drop
-- actually takes effect, turning a silent hole into a refusal to boot.

-- +goose StatementBegin
do $$
declare
    owned text;
begin
    if not exists (select 1 from pg_roles where rolname = 'hackertracker_app') then
        create role hackertracker_app nologin nobypassrls;
    end if;

    -- The connecting (superuser) role must be a member to SET ROLE to it.
    execute format('grant hackertracker_app to %I', current_user);

    execute format('grant usage on schema public to hackertracker_app');
    execute format('grant select, insert, update, delete on all tables in schema public to hackertracker_app');
    execute format('grant usage, select on all sequences in schema public to hackertracker_app');

    -- Tables added by later migrations inherit the same grants.
    execute format('alter default privileges in schema public grant select, insert, update, delete on tables to hackertracker_app');
    execute format('alter default privileges in schema public grant usage, select on sequences to hackertracker_app');
end $$;
-- +goose StatementEnd

-- ── row-level security ───────────────────────────────────────────────────
--
-- The characteristic multi-tenant bug is one forgotten WHERE clause, and here
-- that would mean showing someone another person's review evidence. These
-- policies make the database refuse, so a missed filter returns nothing rather
-- than everything.
--
-- FORCE is required: without it the table owner bypasses its own policies. It
-- is necessary but NOT sufficient — see the role above, since a superuser
-- ignores policies regardless of FORCE.
--
-- current_setting(..., true) yields NULL when unset, and `account_id = NULL`
-- matches no rows. Failing closed is the point: a connection that forgot to
-- identify itself sees nothing.
--
-- The identity tables above are deliberately NOT covered. Sessions and users
-- must be readable before the account is known, so protecting them with RLS
-- would be circular; they are reached only through internal/auth.

-- +goose StatementBegin
do $$
declare
    owned text;
begin
    foreach owned in array array[
        'source_accounts', 'sync_state', 'ingest_log', 'raw_records', 'events',
        'annotations', 'projects', 'project_entries', 'project_events',
        'tags', 'event_tags', 'project_tags'
    ] loop
        execute format('alter table %I enable row level security', owned);
        execute format('alter table %I force row level security', owned);
        execute format($p$
            create policy account_isolation on %I
            using (account_id = nullif(current_setting('app.account_id', true), '')::uuid)
            with check (account_id = nullif(current_setting('app.account_id', true), '')::uuid)
        $p$, owned);
    end loop;
end $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
do $$
begin
    if exists (select 1 from pg_roles where rolname = 'hackertracker_app') then
        execute format('alter default privileges in schema public revoke select, insert, update, delete on tables from hackertracker_app');
        execute format('alter default privileges in schema public revoke usage, select on sequences from hackertracker_app');
        execute 'revoke all on all sequences in schema public from hackertracker_app';
        execute 'revoke all on all tables in schema public from hackertracker_app';
        execute 'revoke usage on schema public from hackertracker_app';
    end if;
end $$;
-- +goose StatementEnd

-- +goose StatementBegin
do $$
declare
    owned text;
begin
    foreach owned in array array[
        'source_accounts', 'sync_state', 'ingest_log', 'raw_records', 'events',
        'annotations', 'projects', 'project_entries', 'project_events',
        'tags', 'event_tags', 'project_tags'
    ] loop
        execute format('drop policy if exists account_isolation on %I', owned);
        execute format('alter table %I disable row level security', owned);
        execute format('alter table %I drop column account_id', owned);
    end loop;
end $$;
-- +goose StatementEnd

alter table source_accounts add constraint source_accounts_source_label_key unique (source, label);
alter table tags add constraint tags_name_key unique (name);

drop table if exists auth_states;
drop table if exists sessions;
drop table if exists user_identities;
drop table if exists users;
drop table if exists accounts;
