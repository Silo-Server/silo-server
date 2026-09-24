-- +goose Up
-- A profile with no ui.disabled_library_ids or ui.next_up_mode row used to fall
-- back, on every request that resolved it, to the account's legacy
-- disabled_library_ids or next_up_mode row in user_settings. The legacy
-- settings endpoint stopped accepting both keys at the settings cutover, so
-- those values are frozen. This copies each one onto every profile on the
-- account that has no row for the canonical key, so the server can stop
-- reading user_settings and each profile keeps the value the fallback gave it.
--
-- The conversion is settingsmigrate.PlanRetiredFallback, which the SQLite user
-- store runs in Go at schema v26. TestRetiredSettingsFallbacksMigrationMatchesPlanner
-- checks this file against it. A value the settings contract cannot store is
-- skipped. ON CONFLICT DO NOTHING keeps every stored row, which the fallback
-- never overrode, and makes a re-run a no-op. The legacy rows stay in
-- user_settings, like every other row the cutover converted.

-- Hidden libraries. The planner decodes the value the way the fallback did,
-- into a Go []int with encoding/json. The pattern is exactly the input that
-- decode accepts: a JSON array of integer literals and nulls. Anything else
-- (malformed JSON, a non-array, fractions, exponents, strings) decodes to
-- nothing, and so does an array holding any id outside int64. A literal of 20
-- or more digits is always outside int64, so the pattern stops at 19 digits,
-- which also keeps the jsonb cast within numeric's range; the decodable check
-- covers the rest. Nulls and non-positive ids are ignored, and a repeat hid
-- nothing extra. The contract then requires unique ids, kept here in
-- first-seen order, and at most 512 of them. A longer list, which needs more
-- than 512 distinct libraries, is not copied, and those libraries become
-- visible.
WITH legacy_ids AS (
    SELECT s.user_id,
           e.ord,
           CASE jsonb_typeof(e.id) WHEN 'number' THEN e.id::numeric END AS id
      FROM user_settings s
     CROSS JOIN LATERAL jsonb_array_elements(
               CASE WHEN s.value ~ '^[ \t\n\r]*\[[ \t\n\r]*((-?(0|[1-9][0-9]{0,18})|null)([ \t\n\r]*,[ \t\n\r]*(-?(0|[1-9][0-9]{0,18})|null))*)?[ \t\n\r]*\][ \t\n\r]*$'
                    THEN s.value::jsonb
               END
           ) WITH ORDINALITY AS e(id, ord)
     WHERE s.key = 'disabled_library_ids'
),
decodable AS (
    SELECT user_id
      FROM legacy_ids
     GROUP BY user_id
    HAVING bool_and(id IS NULL OR id BETWEEN -9223372036854775808 AND 9223372036854775807)
),
hidden AS (
    SELECT l.user_id, l.id, min(l.ord) AS first_seen
      FROM legacy_ids l
      JOIN decodable USING (user_id)
     WHERE l.id > 0
     GROUP BY l.user_id, l.id
),
planned AS (
    SELECT user_id, jsonb_agg(id ORDER BY first_seen) AS value
      FROM hidden
     GROUP BY user_id
    HAVING count(*) <= 512
)
INSERT INTO user_setting_values (user_id, key, scope, profile_id, value)
SELECT p.user_id, 'ui.disabled_library_ids', 'profile', p.id, planned.value
  FROM planned
  JOIN user_profiles p ON p.user_id = planned.user_id
ON CONFLICT (user_id, profile_id, key) WHERE scope = 'profile' DO NOTHING;

-- Next-up mode. The fallback returned the stored string unchanged and the
-- section fetchers matched it exactly against each member, so only an exact
-- member took effect. A padded or unknown value showed next-up in neither
-- place, a state the contract cannot hold, so it is not copied and those
-- profiles get the default, combined.
INSERT INTO user_setting_values (user_id, key, scope, profile_id, value)
SELECT p.user_id, 'ui.next_up_mode', 'profile', p.id, to_jsonb(s.value)
  FROM user_settings s
  JOIN user_profiles p ON p.user_id = s.user_id
 WHERE s.key = 'next_up_mode'
   AND s.value IN ('combined', 'separate')
ON CONFLICT (user_id, profile_id, key) WHERE scope = 'profile' DO NOTHING;

-- +goose Down
-- The copied rows stay. Each one equals what an older binary's fallback derives
-- from the untouched legacy row, so a downgraded server resolves the same
-- answer with or without it, and deleting rows by value could also delete a
-- matching value a user chose afterwards.
SELECT 1;
