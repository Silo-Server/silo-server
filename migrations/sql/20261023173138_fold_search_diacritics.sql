-- +goose NO TRANSACTION

-- +goose Up
-- Search compared titles with their diacritics, so "shogun" missed "Shōgun"
-- whenever another title matched exactly. Folding in normalize_search_text
-- reaches every stored search column; no index is an expression over the
-- function, so refreshing the affected rows is enough and nothing is rebuilt.
-- NORMALIZE and TRANSLATE are built in, so no extension or privilege is needed.
-- +goose StatementBegin
-- Removes diacritics so "Shogun" and "Shōgun" share their search tokens:
-- decompose, drop the combining marks U+0300-U+036F, and recompose what
-- remains (Hangul and kana keep their composed form). Letters with no
-- decomposition (ø, đ, ł, ħ, ı, ŧ, ŀ) map to their base letter and ß, æ and œ
-- are spelled out. ASCII text returns unchanged without the Unicode passes.
-- foldSearchDiacritics in internal/catalog/search_fold.go mirrors this, and
-- TestSearchTextFoldMatchesGoPostgres keeps the two in lockstep.
CREATE OR REPLACE FUNCTION public.fold_search_diacritics(input text)
RETURNS text
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT CASE
    WHEN octet_length(COALESCE(input, '')) = char_length(COALESCE(input, '')) THEN COALESCE(input, '')
    ELSE REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(TRANSLATE(
      NORMALIZE(REGEXP_REPLACE(NORMALIZE(input, NFD), '[' || chr(768) || '-' || chr(879) || ']+', '', 'g'), NFC),
      'ØøĐđÐðŁłĦħıŦŧĿŀ', 'OoDdDdLlHhiTtLl'),
      'ß', 'ss'), 'ẞ', 'SS'), 'æ', 'ae'), 'Æ', 'AE'), 'œ', 'oe'), 'Œ', 'OE')
  END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.normalize_search_text(input text)
RETURNS text
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT COALESCE(
    STRING_AGG(public.normalize_search_number_token(token), ' ' ORDER BY ord),
    ''
  )
  FROM REGEXP_SPLIT_TO_TABLE(
    BTRIM(LOWER(REGEXP_REPLACE(public.fold_search_diacritics(input), '[^[:alnum:]]+', ' ', 'g'))),
    '[[:space:]]+'
  ) WITH ORDINALITY AS tokens(token, ord)
  WHERE token <> '' AND token <> 'and';
$$;
-- +goose StatementEnd

-- Only text with a non-ASCII character can normalize differently, so each
-- loop walks the primary key and rewrites just those rows, committing every
-- batch to bound lock retention and WAL bursts. Rewriting a source column
-- recomputes the generated columns, and clearing the stored vector makes the
-- search-field triggers rebuild each document through normalize_search_text.
-- A resumed run repeats the same idempotent work. The procedure is called as
-- a top-level statement because transaction control is prohibited in a DO.
-- +goose StatementBegin
CREATE OR REPLACE PROCEDURE public.refresh_non_ascii_search_text()
LANGUAGE plpgsql
AS $$
DECLARE
    last_content_id text := '';
    item_ids text[];
    last_alias_id bigint := 0;
    alias_ids bigint[];
    last_folder_id integer := 0;
    last_episode_id text := '';
    folder_ids integer[];
    episode_ids text[];
    retries integer;
BEGIN
    LOOP
        SELECT array_agg(content_id ORDER BY content_id) INTO item_ids
        FROM (
            SELECT content_id FROM public.media_items
            WHERE content_id > last_content_id
              AND octet_length(concat(title, original_title, sort_title))
                  <> char_length(concat(title, original_title, sort_title))
            ORDER BY content_id LIMIT 1000
        ) batch;
        EXIT WHEN item_ids IS NULL;
        UPDATE public.media_items SET title = title, search_title_vector = NULL
        WHERE content_id = ANY(item_ids);
        last_content_id := item_ids[array_length(item_ids, 1)];
        COMMIT;
    END LOOP;

    LOOP
        SELECT array_agg(id ORDER BY id) INTO alias_ids
        FROM (
            SELECT id FROM public.media_item_aliases
            WHERE id > last_alias_id AND octet_length(title) <> char_length(title)
            ORDER BY id LIMIT 1000
        ) batch;
        EXIT WHEN alias_ids IS NULL;
        -- Aliases that differ only by diacritics now share a normalized
        -- title, which idx_media_item_aliases_unique forbids. Of each such
        -- group keep the row whose stored title is already current, since
        -- the refresh leaves it alone, otherwise the oldest row. A keeper
        -- can sit in a later batch, so the check spans the whole table. An
        -- alias another replica commits between the two statements can still
        -- collide; the retry then finds it as the keeper. A batch that keeps
        -- colliding after a few retries fails the migration rather than spin.
        retries := 0;
        LOOP
            BEGIN
                DELETE FROM public.media_item_aliases a
                WHERE a.id = ANY(alias_ids)
                  AND a.normalized_title <> public.normalize_search_text(a.title)
                  AND EXISTS (
                    SELECT 1 FROM public.media_item_aliases b
                    WHERE b.content_id = a.content_id AND b.provider = a.provider
                      AND b.language = a.language AND b.kind = a.kind
                      AND b.snapshot_language = a.snapshot_language
                      AND b.id <> a.id
                      AND public.normalize_search_text(b.title) = public.normalize_search_text(a.title)
                      AND (b.normalized_title = public.normalize_search_text(b.title) OR b.id < a.id)
                  );
                UPDATE public.media_item_aliases SET title = title WHERE id = ANY(alias_ids);
                EXIT;
            EXCEPTION WHEN unique_violation THEN
                retries := retries + 1;
                IF retries > 5 THEN
                    RAISE;
                END IF;
            END;
        END LOOP;
        last_alias_id := alias_ids[array_length(alias_ids, 1)];
        COMMIT;
    END LOOP;

    LOOP
        SELECT array_agg(media_folder_id ORDER BY media_folder_id, episode_id),
               array_agg(episode_id ORDER BY media_folder_id, episode_id)
        INTO folder_ids, episode_ids
        FROM (
            SELECT media_folder_id, episode_id FROM public.episode_catalog_entries
            WHERE (media_folder_id, episode_id) > (last_folder_id, last_episode_id)
              AND octet_length(title) <> char_length(title)
            ORDER BY media_folder_id, episode_id LIMIT 1000
        ) batch;
        EXIT WHEN folder_ids IS NULL;
        UPDATE public.episode_catalog_entries e SET title = e.title, search_title_vector = NULL
        FROM unnest(folder_ids, episode_ids) AS b(media_folder_id, episode_id)
        WHERE e.media_folder_id = b.media_folder_id AND e.episode_id = b.episode_id;
        last_folder_id := folder_ids[array_length(folder_ids, 1)];
        last_episode_id := episode_ids[array_length(episode_ids, 1)];
        COMMIT;
    END LOOP;
END;
$$;
-- +goose StatementEnd
CALL public.refresh_non_ascii_search_text();
DROP PROCEDURE public.refresh_non_ascii_search_text();

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.normalize_search_text(input text)
RETURNS text
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT COALESCE(
    STRING_AGG(public.normalize_search_number_token(token), ' ' ORDER BY ord),
    ''
  )
  FROM REGEXP_SPLIT_TO_TABLE(
    BTRIM(LOWER(REGEXP_REPLACE(COALESCE(input, ''), '[^[:alnum:]]+', ' ', 'g'))),
    '[[:space:]]+'
  ) WITH ORDINALITY AS tokens(token, ord)
  WHERE token <> '' AND token <> 'and';
$$;
-- +goose StatementEnd

DROP FUNCTION IF EXISTS public.fold_search_diacritics(text);

-- Only text with a non-ASCII character can normalize differently, so each
-- loop walks the primary key and rewrites just those rows, committing every
-- batch to bound lock retention and WAL bursts. Rewriting a source column
-- recomputes the generated columns, and clearing the stored vector makes the
-- search-field triggers rebuild each document through normalize_search_text.
-- A resumed run repeats the same idempotent work. The procedure is called as
-- a top-level statement because transaction control is prohibited in a DO.
-- +goose StatementBegin
CREATE OR REPLACE PROCEDURE public.refresh_non_ascii_search_text()
LANGUAGE plpgsql
AS $$
DECLARE
    last_content_id text := '';
    item_ids text[];
    last_alias_id bigint := 0;
    alias_ids bigint[];
    last_folder_id integer := 0;
    last_episode_id text := '';
    folder_ids integer[];
    episode_ids text[];
    retries integer;
BEGIN
    LOOP
        SELECT array_agg(content_id ORDER BY content_id) INTO item_ids
        FROM (
            SELECT content_id FROM public.media_items
            WHERE content_id > last_content_id
              AND octet_length(concat(title, original_title, sort_title))
                  <> char_length(concat(title, original_title, sort_title))
            ORDER BY content_id LIMIT 1000
        ) batch;
        EXIT WHEN item_ids IS NULL;
        UPDATE public.media_items SET title = title, search_title_vector = NULL
        WHERE content_id = ANY(item_ids);
        last_content_id := item_ids[array_length(item_ids, 1)];
        COMMIT;
    END LOOP;

    LOOP
        SELECT array_agg(id ORDER BY id) INTO alias_ids
        FROM (
            SELECT id FROM public.media_item_aliases
            WHERE id > last_alias_id AND octet_length(title) <> char_length(title)
            ORDER BY id LIMIT 1000
        ) batch;
        EXIT WHEN alias_ids IS NULL;
        -- The restored normalization turns a decomposed accent into a word
        -- break, so aliases such as "Poke" + U+0301 + "mon" and "Poke Mon" share a
        -- normalized title again, which idx_media_item_aliases_unique forbids. Of each such
        -- group keep the row whose stored title is already current, since
        -- the refresh leaves it alone, otherwise the oldest row. A keeper
        -- can sit in a later batch, so the check spans the whole table. An
        -- alias another replica commits between the two statements can still
        -- collide; the retry then finds it as the keeper. A batch that keeps
        -- colliding after a few retries fails the migration rather than spin.
        retries := 0;
        LOOP
            BEGIN
                DELETE FROM public.media_item_aliases a
                WHERE a.id = ANY(alias_ids)
                  AND a.normalized_title <> public.normalize_search_text(a.title)
                  AND EXISTS (
                    SELECT 1 FROM public.media_item_aliases b
                    WHERE b.content_id = a.content_id AND b.provider = a.provider
                      AND b.language = a.language AND b.kind = a.kind
                      AND b.snapshot_language = a.snapshot_language
                      AND b.id <> a.id
                      AND public.normalize_search_text(b.title) = public.normalize_search_text(a.title)
                      AND (b.normalized_title = public.normalize_search_text(b.title) OR b.id < a.id)
                  );
                UPDATE public.media_item_aliases SET title = title WHERE id = ANY(alias_ids);
                EXIT;
            EXCEPTION WHEN unique_violation THEN
                retries := retries + 1;
                IF retries > 5 THEN
                    RAISE;
                END IF;
            END;
        END LOOP;
        last_alias_id := alias_ids[array_length(alias_ids, 1)];
        COMMIT;
    END LOOP;

    LOOP
        SELECT array_agg(media_folder_id ORDER BY media_folder_id, episode_id),
               array_agg(episode_id ORDER BY media_folder_id, episode_id)
        INTO folder_ids, episode_ids
        FROM (
            SELECT media_folder_id, episode_id FROM public.episode_catalog_entries
            WHERE (media_folder_id, episode_id) > (last_folder_id, last_episode_id)
              AND octet_length(title) <> char_length(title)
            ORDER BY media_folder_id, episode_id LIMIT 1000
        ) batch;
        EXIT WHEN folder_ids IS NULL;
        UPDATE public.episode_catalog_entries e SET title = e.title, search_title_vector = NULL
        FROM unnest(folder_ids, episode_ids) AS b(media_folder_id, episode_id)
        WHERE e.media_folder_id = b.media_folder_id AND e.episode_id = b.episode_id;
        last_folder_id := folder_ids[array_length(folder_ids, 1)];
        last_episode_id := episode_ids[array_length(episode_ids, 1)];
        COMMIT;
    END LOOP;
END;
$$;
-- +goose StatementEnd
CALL public.refresh_non_ascii_search_text();
DROP PROCEDURE public.refresh_non_ascii_search_text();
