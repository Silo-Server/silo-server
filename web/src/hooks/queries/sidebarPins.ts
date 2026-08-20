import { useMemo, useCallback } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { SidebarPin, SidebarPins } from "@/api/types";
import { captureProfileRequestContext, isProfileRequestContextCurrent } from "@/api/client";
import { storage } from "@/utils/storage";
import { randomUUID } from "@/lib/uuid";
import { SETTING_KEYS } from "@/lib/settingsContract";
import {
  effectiveSettingsQueryKey,
  settingsCapabilitiesSupportAtomicShortcuts,
  settingsCapabilitiesSupportKey,
  useEffectiveSettings,
  useSetNavigationShortcutPresence,
  useSettingsCapabilities,
  type EffectiveSettingsMap,
} from "./settingValues";
import {
  menuItemKey,
  parseShortcuts,
  type ShortcutDocument,
  type ShortcutTarget,
} from "@/lib/uiCustomization";

const PINS_KEYS = [SETTING_KEYS.NAV_SHORTCUTS] as const;
const LEGACY_PINS_KEYS = [SETTING_KEYS.UI_SIDEBAR_PINS] as const;

let nextSidebarPinsRevision = 0;

interface SidebarPinsWriteQueue {
  tail: Promise<unknown>;
}

// Several sidebar/card instances can expose the same pin. Their hook-local
// callbacks still share one ordered stream per query client and active-profile
// cache key, so a later remove cannot overtake an earlier add and one instance
// cannot clear another's optimistic overlay while it still has work queued.
const sidebarPinsWriteQueues = new WeakMap<object, Map<string, SidebarPinsWriteQueue>>();

function sidebarPinsWriteQueue(queryClient: object, pinsQueryKey: readonly unknown[]) {
  let queues = sidebarPinsWriteQueues.get(queryClient);
  if (!queues) {
    queues = new Map();
    sidebarPinsWriteQueues.set(queryClient, queues);
  }
  const key = JSON.stringify(pinsQueryKey);
  let queue = queues.get(key);
  if (!queue) {
    queue = { tail: Promise.resolve() };
    queues.set(key, queue);
  }
  return { queue, queues, key };
}

function sidebarPinsOverlayQueryKey(profileId?: string) {
  return [
    ...effectiveSettingsQueryKey({ keys: PINS_KEYS, profileId }),
    "optimistic-overlay",
  ] as const;
}

/**
 * A separate reactive overlay keeps local whole-document edits stable while
 * mutation events or unrelated invalidations refetch the server value. The
 * overlay is cleared only after the serialized write queue drains and a final
 * refetch has completed.
 */
function useSidebarPinsOverlay() {
  return useQuery<ShortcutDocument | null>({
    queryKey: sidebarPinsOverlayQueryKey(),
    queryFn: async () => null,
    initialData: null,
    enabled: false,
    staleTime: Infinity,
  });
}

/**
 * Accepts the canonical object value, the legacy JSON-string encoding, or
 * null/undefined (the contract default), and always lands on a usable map.
 */
export function parseSidebarPins(value: unknown): SidebarPins {
  if (value == null) return {};
  let parsed: unknown = value;
  if (typeof value === "string") {
    if (!value) return {};
    try {
      parsed = JSON.parse(value);
    } catch {
      return {};
    }
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return {};

  // Revision 5 promotes web-only sidebar pins into the shared shortcut
  // catalog. Convert the flat cross-client shape back to the grouped view the
  // existing sidebar renders; library shortcuts live in the primary menu and
  // therefore do not appear as children of themselves.
  if ("items" in parsed && Array.isArray((parsed as { items?: unknown }).items)) {
    const grouped: SidebarPins = {};
    for (const item of parseShortcuts(parsed).items) {
      if (item.type === "library" || item.library_id === undefined) continue;
      const key = String(item.library_id);
      const pin: SidebarPin =
        item.type === "section"
          ? { type: "section", id: item.section_id, label: item.label }
          : { type: "collection", id: item.collection_id, label: item.label };
      grouped[key] = [...(grouped[key] ?? []), pin];
    }
    return grouped;
  }
  return parsed as SidebarPins;
}

export function sidebarPinsToShortcuts(pins: SidebarPins): ShortcutDocument {
  return {
    items: Object.entries(pins).flatMap(([libraryId, entries]) => {
      const id = Number(libraryId);
      if (!Number.isInteger(id) || id <= 0) return [];
      return entries.map((pin) =>
        pin.type === "section"
          ? ({
              type: "section" as const,
              library_id: id,
              section_id: pin.id,
              label: pin.label,
            } as const)
          : ({
              type: "collection" as const,
              library_id: id,
              collection_id: pin.id,
              label: pin.label,
            } as const),
      );
    }),
  };
}

function shortcutFromSidebarPin(libraryId: number, pin: SidebarPin): ShortcutTarget {
  return pin.type === "section"
    ? { type: "section", library_id: libraryId, section_id: pin.id, label: pin.label }
    : { type: "collection", library_id: libraryId, collection_id: pin.id, label: pin.label };
}

function shortcutDocumentFromSidebarValue(value: unknown): ShortcutDocument {
  let parsed = value;
  if (typeof value === "string") {
    try {
      parsed = JSON.parse(value);
    } catch {
      return { items: [] };
    }
  }
  if (
    typeof parsed === "object" &&
    parsed !== null &&
    !Array.isArray(parsed) &&
    "items" in parsed
  ) {
    return parseShortcuts(parsed);
  }
  return sidebarPinsToShortcuts(parseSidebarPins(parsed));
}

export function toggleNavigationShortcut(
  value: unknown,
  libraryId: number,
  pin: SidebarPin,
): ShortcutDocument {
  const document = shortcutDocumentFromSidebarValue(value);
  const target = shortcutFromSidebarPin(libraryId, pin);
  const targetKey = menuItemKey(target);
  const exists = document.items.some((item) => menuItemKey(item) === targetKey);
  return {
    items: exists
      ? document.items.filter((item) => menuItemKey(item) !== targetKey)
      : [...document.items, target],
  };
}

export function setNavigationShortcutPresence(
  value: unknown,
  target: ShortcutTarget,
  present: boolean,
): ShortcutDocument {
  const document = shortcutDocumentFromSidebarValue(value);
  const targetKey = menuItemKey(target);
  const index = document.items.findIndex((item) => menuItemKey(item) === targetKey);
  if (!present) {
    return index < 0
      ? document
      : { items: document.items.filter((item) => menuItemKey(item) !== targetKey) };
  }
  if (index < 0) return { items: [...document.items, target] };
  const items = [...document.items];
  items[index] = target;
  return { items };
}

export function toggleSidebarPins(
  pins: SidebarPins,
  libraryId: number,
  pin: SidebarPin,
): SidebarPins {
  const key = String(libraryId);
  const existing = pins[key] ?? [];
  const idx = existing.findIndex((p) => p.type === pin.type && p.id === pin.id);

  const nextPins = { ...pins };
  if (idx >= 0) {
    const next = existing.filter((_, i) => i !== idx);
    if (next.length === 0) {
      delete nextPins[key];
    } else {
      nextPins[key] = next;
    }
    return nextPins;
  }

  nextPins[key] = [...existing, pin];
  return nextPins;
}

interface SidebarPinsOptimisticMutation {
  previousValue: unknown;
  previousRevision: number | null;
  optimisticValue: SidebarPins;
  optimisticDocument: ShortcutDocument;
  item: ShortcutTarget;
  present: boolean;
  mutationId: string;
  revision: number;
}

export function createSidebarPinsOptimisticMutation({
  currentValue,
  currentRevision,
  libraryId,
  pin,
  revision,
}: {
  currentValue: unknown;
  currentRevision: number | null | undefined;
  libraryId: number;
  pin: SidebarPin;
  revision: number;
}): SidebarPinsOptimisticMutation {
  const item = shortcutFromSidebarPin(libraryId, pin);
  const present = !shortcutDocumentFromSidebarValue(currentValue).items.some(
    (candidate) => menuItemKey(candidate) === menuItemKey(item),
  );
  const optimisticDocument = setNavigationShortcutPresence(currentValue, item, present);

  return {
    previousValue: currentValue ?? null,
    previousRevision: currentRevision ?? null,
    optimisticValue: parseSidebarPins(optimisticDocument),
    optimisticDocument,
    item,
    present,
    mutationId: randomUUID(),
    revision,
  };
}

export function rollbackSidebarPinsOptimisticMutation({
  currentRevision,
  mutation,
}: {
  currentRevision: number | null | undefined;
  mutation: SidebarPinsOptimisticMutation;
}): { value: unknown; revision: number | null } | null {
  if ((currentRevision ?? null) !== mutation.revision) {
    return null;
  }

  return {
    value: mutation.previousValue,
    revision: mutation.previousRevision,
  };
}

function useHasActiveProfile() {
  // The effective endpoint requires a profile header; before a profile is
  // chosen there is nothing to resolve and nothing to pin.
  return Boolean(storage.get(storage.KEYS.PROFILE_ID));
}

export function useSidebarPins() {
  const hasActiveProfile = useHasActiveProfile();
  const capabilities = useSettingsCapabilities();
  const supportsShortcuts = settingsCapabilitiesSupportKey(
    capabilities.data,
    SETTING_KEYS.NAV_SHORTCUTS,
  );
  const supportsLegacyPins = settingsCapabilitiesSupportKey(
    capabilities.data,
    SETTING_KEYS.UI_SIDEBAR_PINS,
  );
  const enabled = hasActiveProfile && (supportsShortcuts || supportsLegacyPins);
  const keys = supportsShortcuts ? PINS_KEYS : LEGACY_PINS_KEYS;
  const { data, isLoading } = useEffectiveSettings({ keys, enabled });
  const { data: optimisticDocument } = useSidebarPinsOverlay();
  const value = enabled
    ? supportsShortcuts
      ? (optimisticDocument ?? data?.[SETTING_KEYS.NAV_SHORTCUTS]?.value)
      : data?.[SETTING_KEYS.UI_SIDEBAR_PINS]?.value
    : undefined;
  const pins = useMemo(() => parseSidebarPins(value), [value]);
  return { pins, isLoading: capabilities.isLoading || (enabled && isLoading) };
}

export function useToggleSidebarPin() {
  const hasActiveProfile = useHasActiveProfile();
  const capabilities = useSettingsCapabilities();
  const supportsShortcuts = settingsCapabilitiesSupportKey(
    capabilities.data,
    SETTING_KEYS.NAV_SHORTCUTS,
  );
  const canToggle =
    hasActiveProfile && settingsCapabilitiesSupportAtomicShortcuts(capabilities.data);
  const enabled = hasActiveProfile && supportsShortcuts;
  const { data } = useEffectiveSettings({ keys: PINS_KEYS, enabled });
  const renderValue = data?.[SETTING_KEYS.NAV_SHORTCUTS]?.value;
  const queryClient = useQueryClient();
  const setShortcutPresence = useSetNavigationShortcutPresence();
  const overlayQueryKey = sidebarPinsOverlayQueryKey();

  /**
   * Writes are chained rather than fired concurrently.
   *
   * The server merges each desired-state operation atomically across clients.
   * This local chain still matters for rapid toggles on one browser: it keeps
   * add/remove intent in click order and gives optimistic rollback a stable
   * predecessor. The overlay prevents event-driven intermediate refetches
   * from replacing newer local intent while that queue is draining.
   */
  const readCachedValue = useCallback(() => {
    const overlay = queryClient.getQueryData<ShortcutDocument | null>(overlayQueryKey);
    if (overlay) return overlay;
    const cached = queryClient.getQueryData<EffectiveSettingsMap>(
      effectiveSettingsQueryKey({ keys: PINS_KEYS }),
    )?.[SETTING_KEYS.NAV_SHORTCUTS];
    return cached !== undefined ? cached.value : renderValue;
  }, [overlayQueryKey, queryClient, renderValue]);

  const isPinned = useCallback(
    (libraryId: number, pinType: SidebarPin["type"], targetId: string): boolean => {
      const pins = parseSidebarPins(readCachedValue());
      const key = String(libraryId);
      return (pins[key] ?? []).some((p) => p.type === pinType && p.id === targetId);
    },
    [readCachedValue],
  );

  const togglePin = useCallback(
    (libraryId: number, pin: SidebarPin) => {
      if (!canToggle) return;
      const profileAuth = captureProfileRequestContext();
      if (!profileAuth) return;
      const profileId = profileAuth.profileId;
      const pinsQueryKey = effectiveSettingsQueryKey({ keys: PINS_KEYS, profileId });
      const operationOverlayQueryKey = sidebarPinsOverlayQueryKey(profileId);
      const revisionKey = [...pinsQueryKey, "optimistic-revision"] as const;
      const { queue, queues, key: queueKey } = sidebarPinsWriteQueue(queryClient, pinsQueryKey);
      const previousEntry =
        queryClient.getQueryData<EffectiveSettingsMap>(pinsQueryKey)?.[SETTING_KEYS.NAV_SHORTCUTS];
      const previousOverlay =
        queryClient.getQueryData<ShortcutDocument | null>(operationOverlayQueryKey) ?? null;
      const cachedRevision = queryClient.getQueryData<number | null>(revisionKey) ?? null;
      nextSidebarPinsRevision += 1;
      const mutation = createSidebarPinsOptimisticMutation({
        currentValue:
          previousOverlay ?? (previousEntry !== undefined ? previousEntry.value : renderValue),
        currentRevision: cachedRevision,
        libraryId,
        pin,
        revision: nextSidebarPinsRevision,
      });

      queryClient.setQueryData(operationOverlayQueryKey, mutation.optimisticDocument);
      queryClient.setQueryData(revisionKey, mutation.revision);
      const queuedWrite = queue.tail
        .catch(() => undefined)
        .then(() => {
          if (!isProfileRequestContextCurrent(profileAuth)) return;
          return setShortcutPresence.mutateAsync({
            item: mutation.item,
            present: mutation.present,
            mutationId: mutation.mutationId,
            profileAuth,
            invalidateOnSettled: false,
          });
        })
        .catch(() => {
          // Account/server switches clear user-scoped query state. Never put
          // this old account's optimistic predecessor back into aliased keys.
          if (!isProfileRequestContextCurrent(profileAuth)) return;
          const rollback = rollbackSidebarPinsOptimisticMutation({
            currentRevision: queryClient.getQueryData<number | null>(revisionKey),
            mutation,
          });

          if (!rollback) {
            return;
          }

          queryClient.setQueryData(operationOverlayQueryKey, previousOverlay);
          queryClient.setQueryData(revisionKey, rollback.revision);
        });

      const settledWrite: Promise<unknown> = queuedWrite.finally(async () => {
        if (queue.tail !== settledWrite) return;
        if (!isProfileRequestContextCurrent(profileAuth)) {
          if (queues.get(queueKey) === queue) queues.delete(queueKey);
          return;
        }
        await queryClient.invalidateQueries({ queryKey: pinsQueryKey, exact: true });
        if (queue.tail !== settledWrite) return;
        queryClient.setQueryData(operationOverlayQueryKey, null);
        queryClient.setQueryData(revisionKey, null);
        if (queues.get(queueKey) === queue) queues.delete(queueKey);
      });
      queue.tail = settledWrite;
    },
    [canToggle, queryClient, renderValue, setShortcutPresence],
  );

  return { togglePin, isPinned, canToggle };
}
