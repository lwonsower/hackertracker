-- +goose Up

-- A 'review' source holds credentials and fetches like a pull source, but
-- never writes on its own: it proposes, and only what a person picks is
-- stored. Kept out of 'pull' so the startup syncer cannot touch it.
alter table source_accounts drop constraint source_accounts_mode_check;
alter table source_accounts add constraint source_accounts_mode_check
    check (mode in ('pull', 'push', 'manual', 'import', 'review'));

-- Which handshake a state row belongs to. Without it a sign-in code could be
-- replayed against the calendar callback, or the reverse.
alter table auth_states add column purpose text not null default 'signin';

-- +goose Down
alter table auth_states drop column if exists purpose;

alter table source_accounts drop constraint source_accounts_mode_check;
alter table source_accounts add constraint source_accounts_mode_check
    check (mode in ('pull', 'push', 'manual', 'import'));
