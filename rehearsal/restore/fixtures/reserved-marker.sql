-- Deliberate untrusted marker provenance: this schema must be refused.
CREATE SCHEMA rehearsal_guard;
CREATE TABLE rehearsal_guard.clone_marker (
  clone_id text, database_name text, base_revision text
);
