-- +goose Up
-- +goose StatementBegin
-- Kind-leading covering index for the author/narrator (and actor/director)
-- browse-filter facets. Without it, `SELECT DISTINCT p.name ... WHERE
-- ip.kind = $1` ordered by p.name drove the planner to scan the entire people
-- table (~80K rows) probing inward. With a (kind, content_id, person_id) index
-- the facet resolves from an index-only scan of just that kind's credits,
-- joined to the scoped media_items — roughly halving the audiobook
-- narrator/author facet latency on a large library.
CREATE INDEX IF NOT EXISTS idx_item_people_kind_content_person
ON public.item_people USING btree (kind, content_id, person_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_item_people_kind_content_person;
-- +goose StatementEnd
