-- Run only on the shared stack's maintenance database. The lock serialises
-- claims and database DDL, including when two worktrees pick the same name.
SET client_min_messages = warning;
SELECT pg_advisory_lock(hashtext('open-fimra:worktree-databases')) AS lock \gset
CREATE TABLE IF NOT EXISTS fimra_worktree_databases (
    name text PRIMARY KEY,
    worktree text NOT NULL
);

SELECT :'action' IN ('create', 'recreate', 'reset') AS claiming \gset
\if :claiming
    -- Never adopt an existing database that has no worktree record.
    INSERT INTO fimra_worktree_databases (name, worktree)
    SELECT :'base', :'worktree'
    WHERE NOT EXISTS (
        SELECT FROM pg_database WHERE datname = :'base' OR starts_with(datname, :'base' || '__')
    )
    ON CONFLICT DO NOTHING;
\endif

SELECT EXISTS (SELECT FROM fimra_worktree_databases WHERE name = :'base' AND worktree = :'worktree') AS owned,
       EXISTS (SELECT FROM fimra_worktree_databases WHERE name = :'base') OR
       EXISTS (SELECT FROM pg_database WHERE datname = :'base' OR starts_with(datname, :'base' || '__')) AS taken,
       :'action' IN ('drop', 'recreate', 'reset') AS dropping,
       :'action' = 'drop' AS dropping_only
\gset

\if :owned
    \if :dropping
        SELECT format('DROP DATABASE IF EXISTS %I WITH (FORCE)', name)
        FROM unnest(string_to_array(:'databases', ',')) AS name \gexec
    \endif
    \if :claiming
        SELECT format('CREATE DATABASE %I', name)
        FROM unnest(string_to_array(:'databases', ',')) AS name
        WHERE (:'action' = 'recreate' OR name = :'base')
          AND NOT EXISTS (SELECT FROM pg_database WHERE datname = name) \gexec
    \endif
    \if :dropping_only
        DELETE FROM fimra_worktree_databases
        WHERE name = :'base' AND NOT EXISTS (
            SELECT FROM pg_database WHERE datname = :'base' OR starts_with(datname, :'base' || '__')
        );
    \endif
\else
    SELECT :'taken'::boolean OR NOT :'dropping_only'::boolean AS refuse \gset
    \if :refuse
        DO $$ BEGIN
            RAISE EXCEPTION 'Database namespace is unavailable to this worktree. Run make worktree-db or choose a unique FIMRA_DB_NAME.';
        END $$;
    \endif
\endif
