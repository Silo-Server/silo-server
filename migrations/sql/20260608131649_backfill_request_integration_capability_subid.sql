-- +goose Up
-- +goose StatementBegin
-- request_integrations.capability_id must carry the capability SUB-ID
-- ("arr"/"seerr"), matching the value the host passes to
-- requireCapability("request_router.v1", id) when dispensing the plugin. Rows
-- created before the fix stored the capability TYPE "request_router.v1", which
-- resolves to no plugin capability (every save/options/fulfill call 500s with
-- "no fulfillment backend configured" / ErrCapabilityNotFound).
--
-- Backfill the sub-id from each row's bound installation's request_router.v1
-- capability. Rows with installation_id NULL (created under the pre-plugin
-- direct-to-arr system and never re-bound) are left untouched — an admin
-- re-saves them in Admin -> Requests to bind an installation and set the sub-id.
--
-- A deterministic correlated subquery (ORDER BY ... LIMIT 1) is used instead of a
-- join so that, in the unusual case where one installation exposes more than one
-- request_router.v1 capability, the same sub-id is chosen for every row rather
-- than letting Postgres pick arbitrarily from the join cross-product.
UPDATE public.request_integrations ri
SET capability_id = (
    SELECT pc.capability_id
    FROM public.plugin_capabilities pc
    WHERE pc.plugin_installation_id = ri.installation_id
      AND pc.capability_type = 'request_router.v1'
    ORDER BY pc.capability_id
    LIMIT 1
)
WHERE ri.capability_id = 'request_router.v1'
  AND ri.installation_id IS NOT NULL
  AND EXISTS (
    SELECT 1
    FROM public.plugin_capabilities pc
    WHERE pc.plugin_installation_id = ri.installation_id
      AND pc.capability_type = 'request_router.v1'
  );

-- Drop the misleading column default (the capability type). The application now
-- always supplies the sub-id on insert; the default would silently reintroduce
-- the unresolvable type for any row that omitted capability_id.
ALTER TABLE public.request_integrations
    ALTER COLUMN capability_id DROP DEFAULT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Restore the prior column default. The data backfill is intentionally not
-- reverted: the sub-id is the correct value, and rewriting it back to the
-- capability type would re-break fulfillment.
ALTER TABLE public.request_integrations
    ALTER COLUMN capability_id SET DEFAULT 'request_router.v1';
-- +goose StatementEnd
