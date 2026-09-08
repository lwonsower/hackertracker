-- +goose Up

-- Per-account encrypted credentials. Referenced by
-- source_accounts.credentials_ref as "secret:<id>".
create table credentials (
    id           uuid primary key default gen_random_uuid(),
    account_id   uuid not null references accounts on delete cascade,
    label        text not null,
    key_id       text not null,
    ciphertext   bytea not null,
    created_at   timestamptz not null default now(),
    last_used_at timestamptz
);

create index credentials_account_idx on credentials (account_id);

alter table credentials enable row level security;
alter table credentials force row level security;

create policy account_isolation on credentials
    using (account_id = nullif(current_setting('app.account_id', true), '')::uuid)
    with check (account_id = nullif(current_setting('app.account_id', true), '')::uuid);

grant select, insert, update, delete on credentials to hackertracker_app;

-- +goose Down
drop table if exists credentials;
