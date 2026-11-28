import type { useSettingsForm } from "@/hooks/useSettingsForm";

export const REDIS_KEYS = ["redis.url", "redis.db"];

/** The part of a settings form the Redis fields stage their edits through. */
type RedisDraft = Pick<
  ReturnType<typeof useSettingsForm>,
  "getValue" | "getPersistedValue" | "setValue" | "resetValue" | "isDirty"
>;

/**
 * The Database number a URL edit staged, or null. Each form keeps one (a
 * ref) for as long as it lives, so taking back the URL can tell that number
 * from one the admin typed after the settings snapshot has moved.
 */
export interface StagedRedisDb {
  current: string | null;
}

/**
 * Stages an edit of the Redis connection URL and keeps the Database number
 * field true to what the save stores. The field shows the number in use,
 * which is the one in the saved URL while no `redis.db` is saved, so a new URL
 * on its own would move the server to the number that URL names. The number on
 * screen is therefore staged with the URL, and the save and the connection
 * check send both. That includes an empty URL, which switches Redis off: the
 * server stores no number without a URL, and the number stays on screen for a
 * URL typed after it.
 */
export function stageRedisUrl(draft: RedisDraft, url: string, staged: StagedRedisDb) {
  draft.setValue("redis.url", url);
  const shown = draft.getValue("redis.db");
  // The URL edit claims the number only when nothing has staged it (it is the
  // saved one), or an earlier keystroke of this edit claimed it: a number the
  // admin staged first stays theirs, even once the settings report it as
  // saved, so taking the URL back leaves it staged.
  const claimed =
    (staged.current !== null && shown === staged.current) || !draft.isDirty("redis.db");
  staged.current = shown === "" || !claimed ? null : shown;
  if (shown !== "") draft.setValue("redis.db", shown, { stage: true });
}

/**
 * Stages the admin's edit of the Database number. The number is theirs from
 * then on: taking back a URL edit no longer withdraws it.
 */
export function stageRedisDb(
  draft: Pick<RedisDraft, "setValue">,
  value: string,
  staged: StagedRedisDb,
) {
  draft.setValue("redis.db", value);
  staged.current = null;
}

/**
 * Withdraws a staged edit of the Redis connection URL, and with it a number
 * that was staged only to go with that edit, even when the settings have
 * since reported another number. A number the admin changed stays staged.
 */
export function keepSavedRedisUrl(draft: RedisDraft, staged: StagedRedisDb) {
  draft.resetValue("redis.url");
  const shown = draft.getValue("redis.db");
  if (shown === staged.current || shown === draft.getPersistedValue("redis.db")) {
    draft.resetValue("redis.db");
  }
  staged.current = null;
}
