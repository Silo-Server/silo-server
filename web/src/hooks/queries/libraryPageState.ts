import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ApiClientError } from "@/api/client";
import {
  isDefinitiveSettingMutationRejection,
  useEffectiveSettings,
  useSetSettingValue,
} from "@/hooks/queries/settingValues";
import { useOptionalAuth } from "@/hooks/useAuth";
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
  ownerKey: string | null;
  ownerProfileId: string | null;
  subscribers: number;
  resolvedPreference: LibraryPageStatePreference;
  deferredPreference: LibraryPageStatePreference | null;
  unconfirmedWrites: PendingPreferenceWrite[];
  pendingWrites: PendingPreferenceWrite[];
  writeChain: Promise<unknown>;
}

interface PreferenceWriteLease {
  ownerKey: string | null;
  active: boolean;
}

interface PendingPreferenceWrite {
  libraryId: number;
  search: string;
  lease: PreferenceWriteLease;
  promise?: Promise<unknown>;
}

// The setting is a whole-document value. Keep one queue per account+profile
// for the lifetime of this browser context so an unmount/remount cannot let a
// newer document race an older in-flight PUT. Profile IDs are not globally
// unique, so the account must be part of the key to prevent logout/login from
// carrying an old account's overlay into a new account's document.
const preferenceWriteQueues = new Map<string, PreferenceWriteQueue>();

function preferenceWriteOwnerKey(
  accountId: number | null,
  profileId: string | null,
): string | null {
  return accountId === null || profileId === null ? null : JSON.stringify([accountId, profileId]);
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
  ownerKey: string | null,
  ownerProfileId: string | null,
  preference: LibraryPageStatePreference,
): PreferenceWriteQueue {
  return {
    ownerKey,
    ownerProfileId,
    subscribers: 0,
    resolvedPreference: preference,
    deferredPreference: null,
    unconfirmedWrites: [],
    pendingWrites: [],
    writeChain: Promise.resolve(),
  };
}

function getPreferenceWriteQueue(
  ownerKey: string | null,
  ownerProfileId: string | null,
  preference: LibraryPageStatePreference,
): PreferenceWriteQueue {
  if (ownerKey === null) {
    return createPreferenceWriteQueue(null, ownerProfileId, preference);
  }
  const existing = preferenceWriteQueues.get(ownerKey);
  if (existing !== undefined) {
    return existing;
  }
  const queue = createPreferenceWriteQueue(ownerKey, ownerProfileId, preference);
  preferenceWriteQueues.set(ownerKey, queue);
  return queue;
}

function applyPendingPreferenceWrites(
  preference: LibraryPageStatePreference,
  writes: PendingPreferenceWrite[],
): LibraryPageStatePreference {
  return writes.reduce(
    (next, write) => updateLibraryPageStatePreference(next, write.libraryId, write.search),
    preference,
  );
}

function appendPreferenceWrite(
  writes: PendingPreferenceWrite[],
  write: PendingPreferenceWrite,
): PendingPreferenceWrite[] {
  return [...writes.filter((candidate) => candidate.libraryId !== write.libraryId), write];
}

function findMatchingPendingWrite(
  writes: PendingPreferenceWrite[],
  lease: PreferenceWriteLease,
  libraryId: number,
  search: string,
): PendingPreferenceWrite | undefined {
  for (let index = writes.length - 1; index >= 0; index -= 1) {
    const write = writes[index];
    if (write?.libraryId === libraryId) {
      return write.lease === lease && write.lease.active && write.search === search
        ? write
        : undefined;
    }
  }
  return undefined;
}

function removeConfirmedPreferenceWrites(
  preference: LibraryPageStatePreference,
  writes: PendingPreferenceWrite[],
): PendingPreferenceWrite[] {
  return writes.filter(
    (write) => preference.libraries[String(write.libraryId)]?.search !== write.search,
  );
}

function settlePreferenceWrite(
  queue: PreferenceWriteQueue,
  pendingWrite: PendingPreferenceWrite,
  outcome: "success" | "ambiguous_failure" | "definitive_failure",
  attemptedPreference?: LibraryPageStatePreference,
  attemptedWrites: PendingPreferenceWrite[] = [],
) {
  const authoritativePreference = queue.deferredPreference;
  if (outcome === "success" || outcome === "ambiguous_failure") {
    // A successful mutation can still be followed by a stale refetch. Keep
    // every local edit in the overlay until an effective-settings snapshot
    // actually contains it; ambiguous failures need the same protection.
    queue.unconfirmedWrites =
      authoritativePreference === null
        ? attemptedWrites
        : removeConfirmedPreferenceWrites(authoritativePreference, attemptedWrites);
  } else if (authoritativePreference !== null) {
    queue.unconfirmedWrites = removeConfirmedPreferenceWrites(
      authoritativePreference,
      queue.unconfirmedWrites,
    );
  }
  const resolvedBase =
    authoritativePreference ??
    (outcome === "definitive_failure" ? queue.resolvedPreference : attemptedPreference) ??
    queue.resolvedPreference;
  queue.resolvedPreference = applyPendingPreferenceWrites(resolvedBase, queue.unconfirmedWrites);
  queue.deferredPreference = null;
  queue.pendingWrites = queue.pendingWrites.filter((write) => write !== pendingWrite);
}

class LibraryPreferenceWriteCancelledError extends Error {}

function cancelledPreferenceWrite(): Error {
  return new LibraryPreferenceWriteCancelledError(
    "Library preference write cancelled because the active profile changed",
  );
}

export function shouldRetryLibraryPageStateWrite(error: unknown): boolean {
  // A profile switch cancels work owned by the old queue, but the same page
  // state still needs to be submitted through the new profile's queue.
  if (error instanceof LibraryPreferenceWriteCancelledError) {
    return true;
  }
  // 408, 425, and 429 are definitive HTTP responses but transient request
  // outcomes. Keep that retry decision separate from the queue's commit
  // certainty decision so rate limiting cannot make a page state terminal.
  if (error instanceof ApiClientError) {
    return (
      error.status === 408 || error.status === 425 || error.status === 429 || error.status >= 500
    );
  }
  return !isDefinitiveSettingMutationRejection(error);
}

export function libraryPageStateWriteRetryDelay(
  error: unknown,
  fallbackDelayMs: number,
): number | null {
  if (!shouldRetryLibraryPageStateWrite(error)) {
    return null;
  }
  if (error instanceof LibraryPreferenceWriteCancelledError) {
    return 0;
  }
  if (error instanceof ApiClientError && error.status === 429) {
    const body = error.body;
    const retryAfter =
      body && typeof body === "object" && "retry_after" in body
        ? (body as { retry_after?: unknown }).retry_after
        : undefined;
    if (typeof retryAfter === "number" && Number.isFinite(retryAfter) && retryAfter > 0) {
      return Math.max(fallbackDelayMs, retryAfter * 1_000);
    }
  }
  return fallbackDelayMs;
}

export function useLibraryPageStatePreference() {
  // The effective endpoint requires a profile header; before one is chosen
  // there is no saved state to restore and nowhere to save it.
  const auth = useOptionalAuth();
  const activeAccountId = auth?.user?.id ?? null;
  const activeProfileId = storage.get(storage.KEYS.PROFILE_ID);
  const activeOwnerKey = preferenceWriteOwnerKey(activeAccountId, activeProfileId);
  const enabled = activeOwnerKey !== null;
  const { data, isLoading } = useEffectiveSettings({ keys: PAGE_STATE_KEYS, enabled });
  const mutation = useSetSettingValue();
  const { mutateAsync } = mutation;

  const stateValue = data?.[SETTING_KEYS.UI_LIBRARY_PAGE_STATE]?.value;
  const preference = useMemo(() => parseLibraryPageStatePreference(stateValue), [stateValue]);
  // This setting is one last-write-wins document. Keep queued changes in the
  // same document and send them in order so a slower request cannot restore an
  // older library state over a newer one.
  const [initialWriteQueue] = useState(() =>
    getPreferenceWriteQueue(activeOwnerKey, activeProfileId, preference),
  );
  const [initialWriteLease] = useState<PreferenceWriteLease>(() => ({
    ownerKey: activeOwnerKey,
    active: false,
  }));
  const writeQueueRef = useRef(initialWriteQueue);
  const writeLeaseRef = useRef(initialWriteLease);
  useEffect(() => {
    let currentQueue = writeQueueRef.current;
    let currentLease = writeLeaseRef.current;
    if (currentQueue.ownerKey !== activeOwnerKey) {
      currentLease.active = false;
      // The following preference-sync effect supplies this owner's resolved
      // document before callers' effects can enqueue a write.
      currentQueue = getPreferenceWriteQueue(
        activeOwnerKey,
        activeProfileId,
        createEmptyLibraryPageStatePreference(),
      );
      currentLease = { ownerKey: activeOwnerKey, active: false };
      writeQueueRef.current = currentQueue;
      writeLeaseRef.current = currentLease;
    }
    currentQueue.subscribers += 1;
    currentLease.active = true;

    return () => {
      currentLease.active = false;
      currentQueue.subscribers = Math.max(0, currentQueue.subscribers - 1);
    };
  }, [activeOwnerKey, activeProfileId]);
  useEffect(() => {
    const queue = writeQueueRef.current;
    const lease = writeLeaseRef.current;
    if (!lease.active || lease.ownerKey !== activeOwnerKey || queue.ownerKey !== activeOwnerKey) {
      return;
    }
    if (queue.pendingWrites.length === 0) {
      queue.unconfirmedWrites = removeConfirmedPreferenceWrites(
        preference,
        queue.unconfirmedWrites,
      );
      queue.resolvedPreference = applyPendingPreferenceWrites(preference, queue.unconfirmedWrites);
      queue.deferredPreference = null;
    } else {
      // A realtime update or mutation refetch can arrive while a local write
      // is in flight. Retain the newest authoritative snapshot so a rejected
      // local tail cannot make the next write erase it.
      queue.unconfirmedWrites = removeConfirmedPreferenceWrites(
        preference,
        queue.unconfirmedWrites,
      );
      queue.deferredPreference = preference;
    }
  }, [activeOwnerKey, preference]);
  // The contract default is true; anything but an explicit false keeps the
  // feature on, matching the legacy `!== "false"` reading.
  const rememberEnabled = data?.[SETTING_KEYS.UI_REMEMBER_LIBRARY_PAGE_STATE]?.value !== false;
  const saveLibrarySearch = useCallback(
    (libraryId: number, search: string) => {
      const queue = writeQueueRef.current;
      const lease = writeLeaseRef.current;
      if (
        !lease.active ||
        lease.ownerKey !== activeOwnerKey ||
        queue.ownerKey === null ||
        queue.ownerKey !== activeOwnerKey
      ) {
        return Promise.reject(cancelledPreferenceWrite());
      }
      const matchingWrite = findMatchingPendingWrite(queue.pendingWrites, lease, libraryId, search);
      if (matchingWrite?.promise !== undefined) {
        return matchingWrite.promise;
      }

      const pendingWrite: PendingPreferenceWrite = { libraryId, search, lease };
      queue.pendingWrites.push(pendingWrite);

      let attemptedPreference: LibraryPageStatePreference | undefined;
      let attemptedWrites: PendingPreferenceWrite[] = [];
      let mutationStarted = false;
      const write = queue.writeChain
        .catch(() => undefined)
        .then(() => {
          if (!lease.active || storage.get(storage.KEYS.PROFILE_ID) !== queue.ownerProfileId) {
            throw cancelledPreferenceWrite();
          }
          attemptedPreference = updateLibraryPageStatePreference(
            queue.resolvedPreference,
            libraryId,
            search,
          );
          attemptedWrites = appendPreferenceWrite(queue.unconfirmedWrites, pendingWrite);
          mutationStarted = true;
          return mutateAsync({
            key: SETTING_KEYS.UI_LIBRARY_PAGE_STATE,
            value: attemptedPreference,
            identity: DEVICE_SCOPE,
          });
        });
      pendingWrite.promise = write;
      queue.writeChain = write.then(
        (result) => {
          settlePreferenceWrite(
            queue,
            pendingWrite,
            "success",
            attemptedPreference,
            attemptedWrites,
          );
          return result;
        },
        (error: unknown) => {
          settlePreferenceWrite(
            queue,
            pendingWrite,
            !mutationStarted || isDefinitiveSettingMutationRejection(error)
              ? "definitive_failure"
              : "ambiguous_failure",
            attemptedPreference,
            attemptedWrites,
          );
          return undefined;
        },
      );
      return write;
    },
    [activeOwnerKey, mutateAsync],
  );

  return {
    ownerKey: activeOwnerKey,
    isLoading: enabled && isLoading,
    preference,
    rememberEnabled,
    saveLibrarySearch,
  };
}
