-- Each module owns one schema and only that module writes to it.
CREATE SCHEMA IF NOT EXISTS example;

CREATE TABLE example.greetings (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    message    text        NOT NULL CHECK (char_length(message) BETWEEN 1 AND 280),
    created_at timestamptz NOT NULL DEFAULT now()
);
