-- Development database bootstrap. Runs once when the postgres volume is created.
-- Schema migrations are applied by sermon-server (goose, M0-08) – nothing else belongs here.
CREATE EXTENSION IF NOT EXISTS timescaledb;
