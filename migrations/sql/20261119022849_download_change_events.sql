-- +goose Up
-- +goose StatementBegin

-- Outbox for the administrator download.changed events: one row when a
-- managed (device) download is created, changes status or revision, moves to
-- another title or episode (a catalog merge), or is deleted. Triggers write it whatever code path changed the row, including
-- cascades from a deleted account, profile or title; every API server drains
-- it (see internal/downloads/change_outbox.go). Ephemeral web rows, which no
-- administrator route lists, are left out. A row keeps the identifiers so a
-- removal can still name what it removed.
CREATE TABLE public.download_change_events (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    download_id text NOT NULL,
    user_id     integer NOT NULL,
    profile_id  text NOT NULL,
    device_id   text NOT NULL,
    content_id  text NOT NULL,
    episode_id  text,
    status      text NOT NULL,
    change      text NOT NULL CHECK (change IN ('created', 'changed', 'removed')),
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- One row: when a server last drained, so the cluster as a whole publishes
-- at a bounded rate however many servers run.
CREATE TABLE public.download_change_drain (
    singleton  boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    drained_at timestamptz NOT NULL DEFAULT '-infinity'
);
INSERT INTO public.download_change_drain DEFAULT VALUES;

CREATE FUNCTION public.download_change_queue()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        INSERT INTO public.download_change_events
            (download_id, user_id, profile_id, device_id, content_id, episode_id, status, change)
        VALUES (OLD.id, OLD.user_id, coalesce(OLD.profile_id, ''), OLD.device_id, OLD.content_id, OLD.episode_id, OLD.status, 'removed');
        RETURN OLD;
    END IF;
    INSERT INTO public.download_change_events
        (download_id, user_id, profile_id, device_id, content_id, episode_id, status, change)
    VALUES (NEW.id, NEW.user_id, coalesce(NEW.profile_id, ''), NEW.device_id, NEW.content_id, NEW.episode_id, NEW.status,
            CASE WHEN TG_OP = 'INSERT' THEN 'created' ELSE 'changed' END);
    RETURN NEW;
END
$$;

-- Row triggers with WHEN clauses: the frequent updates that change none of
-- status, revision or the title the event names (transfer leases, client
-- addresses, byte counts) cost no function call.
CREATE TRIGGER trg_download_change_insert
AFTER INSERT ON public.downloads
FOR EACH ROW WHEN (NEW.device_id IS NOT NULL)
EXECUTE FUNCTION public.download_change_queue();

CREATE TRIGGER trg_download_change_update
AFTER UPDATE OF status, revision, content_id, episode_id ON public.downloads
FOR EACH ROW WHEN (NEW.device_id IS NOT NULL
    AND (OLD.status IS DISTINCT FROM NEW.status OR OLD.revision IS DISTINCT FROM NEW.revision
        OR OLD.content_id IS DISTINCT FROM NEW.content_id OR OLD.episode_id IS DISTINCT FROM NEW.episode_id))
EXECUTE FUNCTION public.download_change_queue();

CREATE TRIGGER trg_download_change_delete
AFTER DELETE ON public.downloads
FOR EACH ROW WHEN (OLD.device_id IS NOT NULL)
EXECUTE FUNCTION public.download_change_queue();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_download_change_delete ON public.downloads;
DROP TRIGGER IF EXISTS trg_download_change_update ON public.downloads;
DROP TRIGGER IF EXISTS trg_download_change_insert ON public.downloads;
DROP FUNCTION IF EXISTS public.download_change_queue();
DROP TABLE IF EXISTS public.download_change_drain;
DROP TABLE IF EXISTS public.download_change_events;
-- +goose StatementEnd
