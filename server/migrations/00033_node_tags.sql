-- Free tags of nodes (M2-29, KONZEPT §7): key=value pairs such as env=prod for filters and for
-- matching in rules, notification policies and silences – never for rights. The server validates
-- keys and values (domain/tag); the database keeps the shape and an upper bound. The nodes table
-- already has row-level security, so the tags are as isolated as the nodes.

-- +goose Up
ALTER TABLE nodes ADD COLUMN tags jsonb NOT NULL DEFAULT '{}'
    CHECK (jsonb_typeof(tags) = 'object' AND octet_length(tags::text) <= 2048);

-- +goose Down
ALTER TABLE nodes DROP COLUMN tags;
