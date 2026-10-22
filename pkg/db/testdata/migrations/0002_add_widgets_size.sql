-- +goose Up
-- Expand step: a nullable column old code can ignore.
ALTER TABLE widgets ADD COLUMN size_bytes bigint;
