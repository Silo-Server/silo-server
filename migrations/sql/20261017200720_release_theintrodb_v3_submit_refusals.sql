-- Release TheIntroDB contribution claims recorded as permanent refusals when
-- its v3 submit endpoint went away.
--
-- TheIntroDB froze API v3 to GET /media, so POST /v3/submit answers
-- 404 "Cannot POST /v3/submit" for every submission. The host treats a submit
-- 404 as the provider refusing that item and holds the claim for 30 days, so
-- each target a v3 plugin tried in that time would stay blocked after the
-- upgrade to the v4 plugin. API v4's submit endpoint never answers 404, so a
-- TheIntroDB refusal with that status recorded the missing route, not the
-- item. The rows become released retryable errors, as a failed request is
-- recorded, and the next contribution run submits them through v4.

-- +goose Up
-- +goose StatementBegin
UPDATE public.marker_contributions
SET status = 'error',
    claim_active = false
WHERE status = 'invalid'
  AND http_status = 404
  AND (provider = 'introdb' OR provider LIKE 'plugin:%:introdb');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Deliberately empty: a released claim is the state a retryable error leaves,
-- and restoring the refusals would block items for no reason.
SELECT 1;
-- +goose StatementEnd
