-- The rule editor (M2-17) keeps the YAML an admin wrote next to the checked document, so the
-- editor shows comments and layout again; the engine reads only the document.

-- +goose Up
ALTER TABLE alert_rule_overrides ADD COLUMN source text CHECK (octet_length(source) <= 65536);

-- +goose Down
ALTER TABLE alert_rule_overrides DROP COLUMN source;
