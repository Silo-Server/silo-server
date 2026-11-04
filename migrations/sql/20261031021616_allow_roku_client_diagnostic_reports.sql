-- +goose NO TRANSACTION

-- +goose Up
-- The Roku client uploads client diagnostics as the roku platform. macos
-- stays reserved and rejected.
ALTER TABLE public.client_diagnostic_reports
    DROP CONSTRAINT client_diagnostic_reports_platform_check,
    ADD CONSTRAINT client_diagnostic_reports_platform_check
        CHECK (platform IN ('android', 'android-tv', 'ios', 'tvos', 'roku')) NOT VALID;
ALTER TABLE public.client_diagnostic_reports VALIDATE CONSTRAINT client_diagnostic_reports_platform_check;

-- +goose Down
-- The wider constraint is kept. An older binary never accepts a roku report,
-- and narrowing it again would fail while a stored roku report remains.
SELECT 1;
