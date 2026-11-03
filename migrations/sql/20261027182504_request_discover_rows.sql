-- +goose Up
-- +goose StatementBegin
-- The Requests Discover page's layout as the admin set it: an ordered array
-- of rows (internal/requests/discoverrows.Row). One row; none stored means
-- the code's default layout, so a server that never changed it follows
-- future defaults. The revision guards the admin editor like the other
-- request editors.
CREATE TABLE public.request_discover_layout (
    id         boolean PRIMARY KEY DEFAULT true CONSTRAINT request_discover_layout_one_row CHECK (id),
    rows       jsonb NOT NULL CONSTRAINT request_discover_layout_rows_array CHECK (jsonb_typeof(rows) = 'array'),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    revision   bigint NOT NULL DEFAULT nextval('request_editor_revision_seq')
);
CREATE TRIGGER request_discover_layout_revision BEFORE INSERT OR UPDATE ON public.request_discover_layout
    FOR EACH ROW EXECUTE FUNCTION advance_request_editor_revision();

-- Stored copies of the TMDB and MDBList lists Discover rows show, one row per
-- list source ('tmdb_list:<id>' or 'mdblist:<canonical /json URL>'), so every
-- node serves the same titles and a viewer never waits on a list download.
-- entries holds the cards in list order; availability and request state are
-- looked up per viewer. A background task refreshes them; a failed refresh
-- keeps the last good entries.
CREATE TABLE public.request_discover_list_snapshots (
    source_key      text        PRIMARY KEY,
    entries         jsonb       NOT NULL DEFAULT '[]'::jsonb
                    CONSTRAINT request_discover_list_snapshots_entries_array CHECK (jsonb_typeof(entries) = 'array'),
    entry_count     integer     NOT NULL DEFAULT 0,
    refreshed_at    timestamptz,
    last_attempt_at timestamptz,
    last_status     text        NOT NULL DEFAULT ''
                    CONSTRAINT request_discover_list_snapshots_status CHECK (last_status IN ('', 'refreshing', 'ok', 'empty', 'error')),
    last_error      text        NOT NULL DEFAULT ''
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.request_discover_list_snapshots;
DROP TABLE IF EXISTS public.request_discover_layout;
-- +goose StatementEnd
