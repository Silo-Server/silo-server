-- +goose Up
-- Titles a limited profile may watch even though they are above its rating or
-- advisory-age limit. The household's primary profile manages them. An
-- allowance names a movie or series; a series allowance covers its seasons and
-- episodes. It lifts only the maturity limits, never library access.
-- +goose StatementBegin
CREATE TABLE public.profile_title_allowances (
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    content_id text COLLATE "C" NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT profile_title_allowances_pkey PRIMARY KEY (user_id, profile_id, content_id),
    CONSTRAINT profile_title_allowances_profile_fkey FOREIGN KEY (user_id, profile_id)
        REFERENCES public.user_profiles(user_id, id) ON DELETE CASCADE,
    CONSTRAINT profile_title_allowances_content_fkey FOREIGN KEY (content_id)
        REFERENCES public.media_items(content_id) ON UPDATE CASCADE ON DELETE CASCADE
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_profile_title_allowances_content_id
    ON public.profile_title_allowances (content_id);
-- +goose StatementEnd

-- The household manager pages a profile's allowances newest first.
-- +goose StatementBegin
CREATE INDEX idx_profile_title_allowances_profile_created
    ON public.profile_title_allowances (user_id, profile_id, created_at DESC, content_id DESC);
-- +goose StatementEnd

-- Personal collections allowed live for a limited profile: every title in the
-- collection is allowed while it stays a manual collection created by the
-- account's primary profile. Readers check those two conditions, so a
-- collection that stops meeting them stops allowing anything.
-- +goose StatementBegin
CREATE TABLE public.profile_collection_allowances (
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    collection_id text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT profile_collection_allowances_pkey PRIMARY KEY (user_id, profile_id, collection_id),
    CONSTRAINT profile_collection_allowances_profile_fkey FOREIGN KEY (user_id, profile_id)
        REFERENCES public.user_profiles(user_id, id) ON DELETE CASCADE,
    CONSTRAINT profile_collection_allowances_collection_fkey FOREIGN KEY (user_id, collection_id)
        REFERENCES public.user_personal_collections(user_id, id) ON DELETE CASCADE
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.profile_collection_allowances;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS public.profile_title_allowances;
-- +goose StatementEnd
