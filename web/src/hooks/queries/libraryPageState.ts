import { useCallback, useEffect, useMemo, useRef } from "react";
import { useEffectiveSettings, useSetSettingValue } from "@/hooks/queries/settingValues";
import type { SettingIdentity } from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { storage } from "@/utils/storage";

/** Both keys are profile+device scoped in the contract. */
const DEVICE_SCOPE: SettingIdentity = { scope: "profile_device" };

const PAGE_STATE_KEYS = [
  SETTING_KEYS.UI_LIBRARY_PAGE_STATE,
  SETTING_KEYS.UI_REMEMBER_LIBRARY_PAGE_STATE,
] as const;

export interface LibraryPageStatePreference {
  version: 1;
  libraries: Record<string, { search: string }>;
}

interface PreferenceWriteQueue {
  ownerProfileId: string | null;
  active: boolean;
  queuedPreference: LibraryPageStatePreference;
  pendingWriteCount: number;
  writeChain: Promise<unknown>;
  callerTail: Promise<unknown>;
}

/**
 * Accepts the canonical object value, the legacy JSON-string encoding, or
 * null/undefined (the contract default), and always lands on a usable
 * preference.
 */
export function parseLibraryPageStatePreference(raw: unknown): LibraryPageStatePreference {
  if (raw == null) {
    return createEmptyLibraryPageStatePreference();
  }
  let value: unknown = raw;
  if (typeof raw === "string") {
    if (!raw) {
      return createEmptyLibraryPageStatePreference();
    }
    try {
      value = JSON.parse(raw);
    } catch {
      return createEmptyLibraryPageStatePreference();
    }
  }
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return createEmptyLibraryPageStatePreference();
  }
  const maybePreference = value as {
    version?: unknown;
    libraries?: unknown;
  };
  if (maybePreference.version !== 1 || !maybePreference.libraries) {
    return createEmptyLibraryPageStatePreference();
  }
  if (typeof maybePreference.libraries !== "object" || Array.isArray(maybePreference.libraries)) {
    return createEmptyLibraryPageStatePreference();
  }

  const libraries: LibraryPageStatePreference["libraries"] = {};
  Object.entries(maybePreference.libraries).forEach(([libraryId, entry]) => {
    if (!/^\d+$/.test(libraryId) || !entry || typeof entry !== "object" || Array.isArray(entry)) {
      return;
    }
    const search = (entry as { search?: unknown }).search;
    if (typeof search !== "string") {
      return;
    }
    libraries[libraryId] = { search };
  });

  return { version: 1, libraries };
}

export function updateLibraryPageStatePreference(
  preference: LibraryPageStatePreference,
  libraryId: number,
  search: string,
): LibraryPageStatePreference {
  return {
    version: 1,
    libraries: {
      ...preference.libraries,
      [String(libraryId)]: { search },
    },
  };
}

function createEmptyLibraryPageStatePreference(): LibraryPageStatePreference {
  return { version: 1, libraries: {} };
}

function createPreferenceWriteQueue(
  ownerProfileId: string | null,
  preference: LibraryPageStatePreference,
): PreferenceWriteQueue {
  return {
    ownerProfileId,
    active: true,
    queuedPreference: preference,
    pendingWriteCount: 0,
    writeChain: Promise.resolve(),
    callerTail: Promise.resolve(),
  };
}

function cancelledPreferenceWrite(): Error {
  return new Error("Library preference write cancelled because the active profile changed");
}

export function useLibraryPageStatePreference() {
  // The effective endpoint requires a profile header; before one is chosen
  // there is no saved state to restore and nowhere to save it.
  const activeProfileId = storage.get(storage.KEYS.PROFILE_ID);
  const enabled = Boolean(activeProfileId);
  const { data, isLoading } = useEffectiveSettings({ keys: PAGE_STATE_KEYS, enabled });
  const mutation = useSetSettingValue();
  const { mutateAsync } = mutation;

  const stateValue = data?.[SETTING_KEYS.UI_LIBRARY_PAGE_STATE]?.value;
  const preference = useMemo(() => parseLibraryPageStatePreference(stateValue), [stateValue]);
  // This setting is one last-write-wins document. Keep queued changes in the
  // same document and send them in order so a slower request cannot restore an
  // older library state over a newer one.
  const writeQueueRef = useRef(createPreferenceWriteQueue(activeProfileId, preference));
  useEffect(() => {
    let currentQueue = writeQueueRef.current;
    if (currentQueue.ownerProfileId !== activeProfileId) {
      currentQueue.active = false;
      // The following preference-sync effect supplies this owner's resolved
      // document before callers' effects can enqueue a write.
      currentQueue = createPreferenceWriteQueue(
        activeProfileId,
        createEmptyLibraryPageStatePreference(),
      );
      writeQueueRef.current = currentQueue;
    }
    currentQueue.active = true;

    return () => {
      currentQueue.active = false;
    };
  }, [activeProfileId]);
  useEffect(() => {
    const queue = writeQueueRef.current;
    if (
      queue !== null &&
      queue.active &&
      queue.ownerProfileId === activeProfileId &&
      queue.pendingWriteCount === 0
    ) {
      queue.queuedPreference = preference;
    }
  }, [activeProfileId, preference]);
  // The contract default is true; anything but an explicit false keeps the
  // feature on, matching the legacy `!== "false"` reading.
  const rememberEnabled = data?.[SETTING_KEYS.UI_REMEMBER_LIBRARY_PAGE_STATE]?.value !== false;
  const saveLibrarySearch = useCallback(
    (libraryId: number, search: string) => {
      const queue = writeQueueRef.current;
      if (
        !queue.active ||
        queue.ownerProfileId === null ||
        queue.ownerProfileId !== activeProfileId
      ) {
        return Promise.reject(cancelledPreferenceWrite());
      }
      if (
        queue.pendingWriteCount > 0 &&
        queue.queuedPreference.libraries[String(libraryId)]?.search === search
      ) {
        return queue.callerTail;
      }

      const nextPreference = updateLibraryPageStatePreference(
        queue.queuedPreference,
        libraryId,
        search,
      );
      queue.queuedPreference = nextPreference;
      queue.pendingWriteCount += 1;

      const write = queue.writeChain
        .catch(() => undefined)
        .then(() => {
          if (!queue.active || storage.get(storage.KEYS.PROFILE_ID) !== queue.ownerProfileId) {
            throw cancelledPreferenceWrite();
          }
          return mutateAsync({
            key: SETTING_KEYS.UI_LIBRARY_PAGE_STATE,
            value: nextPreference,
            identity: DEVICE_SCOPE,
          });
        });
      queue.callerTail = write;
      queue.writeChain = write.then(
        (result) => {
          queue.pendingWriteCount -= 1;
          return result;
        },
        () => {
          queue.pendingWriteCount -= 1;
          return undefined;
        },
      );
      return write;
    },
    [activeProfileId, mutateAsync],
  );

  return {
    isLoading: enabled && isLoading,
    preference,
    rememberEnabled,
    saveLibrarySearch,
  };
}
