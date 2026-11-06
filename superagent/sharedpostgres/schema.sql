-- Apply once with the host's migration credentials, before opening a journal.
-- Do not drop/recreate domain rows: their durable epochs fence old owners.
CREATE SCHEMA pi_superagent_v1;
CREATE TABLE pi_superagent_v1.domains (
    domain text PRIMARY KEY,
    header bytea NOT NULL,
    owner text NOT NULL,
    epoch bigint NOT NULL CHECK (epoch >= 0),
    expires timestamptz NOT NULL,
    seq bigint NOT NULL CHECK (seq >= 0)
);
CREATE TABLE pi_superagent_v1.transitions (
    domain text NOT NULL REFERENCES pi_superagent_v1.domains(domain),
    seq bigint NOT NULL CHECK (seq > 0),
    record bytea NOT NULL,
    PRIMARY KEY (domain, seq)
);
