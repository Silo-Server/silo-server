-- +goose Up
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_operational_logs_component_level
    ON public.operational_logs (component, level, "timestamp" DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_operational_logs_component_level;
-- +goose StatementEnd
