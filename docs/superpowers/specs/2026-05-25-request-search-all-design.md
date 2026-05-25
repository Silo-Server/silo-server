# Request Search All Design

## Goal

Request search should show movies and series together by default, while still allowing users and clients to filter to only movies or only series.

## Behavior

- `/requests/search` accepts `media_type=all` and treats a missing `media_type` the same way.
- `media_type=movie` and `media_type=series` keep their current filtered behavior.
- Mixed search returns only TMDB movie and TV results. People and other TMDB multi-search result types are excluded.
- Detail pages and request creation stay strict: they continue to accept only `movie` or `series`.

## Architecture

- The TMDB client owns provider-specific mixed search by mapping `all` to TMDB `/search/multi`.
- The request service normalizes search media type separately from detail/create media type validation.
- The Requests page defaults its filter to `All` and sends `media_type=all` for submitted searches.

## Verification

Commands assume the repository root is the cwd.

- Run focused Go tests for `internal/metadata/tmdb` and `internal/requests`.
- Run frontend lint/type verification for the web code touched by the filter type change.
