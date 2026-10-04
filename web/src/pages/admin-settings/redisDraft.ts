import type { useSettingsForm } from "@/hooks/useSettingsForm";

/** The part of a settings form the Redis fields stage their edits through. */
type RedisDraft = Pick<
  ReturnType<typeof useSettingsForm>,
  "getValue" | "getPersistedValue" | "setValue" | "resetValue"
>;

/**
 * Stages an edit of the Redis connection URL and keeps the Database number
 * field true to what the save stores. The field shows the number in use,
 * which is the one in the saved URL while no `redis.db` is saved, so a new URL
 * on its own would move the server to the number that URL names. The number on
 * screen is therefore staged with the URL, and the save and the connection
 * check send both. An empty URL switches Redis off, which takes a saved number
 * with it.
 */
export function stageRedisUrl(draft: RedisDraft, url: string) {
  draft.setValue("redis.url", url);
  if (url === "") {
    if (draft.getPersistedValue("redis.db") !== "") draft.setValue("redis.db", "");
    return;
  }
  const shown = draft.getValue("redis.db");
  if (shown !== "") draft.setValue("redis.db", shown);
}

/**
 * Withdraws a staged edit of the Redis connection URL, and with it a number
 * that was staged only to go with that edit. A number the admin changed stays
 * staged.
 */
export function keepSavedRedisUrl(draft: RedisDraft) {
  draft.resetValue("redis.url");
  if (draft.getValue("redis.db") === draft.getPersistedValue("redis.db")) {
    draft.resetValue("redis.db");
  }
}
