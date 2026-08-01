import { useMemo, useCallback, useRef } from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { SidebarPin, SidebarPins } from "@/api/types";
import { storage } from "@/utils/storage";
import { SETTING_KEYS } from "@/lib/settingsContract";
import {
  effectiveSettingsQueryKey,
  useEffectiveSettings,
  useSetSettingValue,
  type EffectiveSettingsMap,
  type SettingIdentity,
} from "./settingValues";

/** `ui.sidebar_pins` is profile-wide in the contract (no device scope). */
const PROFILE_SCOPE: SettingIdentity = { scope: "profile" };

const PINS_KEYS = [SETTING_KEYS.UI_SIDEBAR_PINS] as const;

let nextSidebarPinsRevision = 0;

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
  return parsed as SidebarPins;
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
  const currentPins = parseSidebarPins(currentValue);
  const nextPins = toggleSidebarPins(currentPins, libraryId, pin);

  return {
    previousValue: currentValue ?? null,
    previousRevision: currentRevision ?? null,
    optimisticValue: nextPins,
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
  const enabled = useHasActiveProfile();
  const { data, isLoading } = useEffectiveSettings({ keys: PINS_KEYS, enabled });
  const value = data?.[SETTING_KEYS.UI_SIDEBAR_PINS]?.value;
  const pins = useMemo(() => parseSidebarPins(value), [value]);
  return { pins, isLoading };
}

export function useToggleSidebarPin() {
  const enabled = useHasActiveProfile();
  const { data } = useEffectiveSettings({ keys: PINS_KEYS, enabled });
  const renderValue = data?.[SETTING_KEYS.UI_SIDEBAR_PINS]?.value;
  const queryClient = useQueryClient();
  const setValue = useSetSettingValue();

  /**
   * Writes are chained rather than fired concurrently.
   *
   * This setting is one whole document, and the server upsert is last-write-
   * wins. Two toggles in flight at once can commit in either order, so the
   * first request landing second restores the document from before the second
   * toggle and silently drops it. The optimistic revision only orders the
   * client-side rollback; it cannot order the server. Chaining keeps at most
   * one request in flight, and because each link reads the document at the
   * moment it runs, a queued toggle sends the latest state rather than the one
   * it was queued with.
   */
  const writeChain = useRef<Promise<unknown>>(Promise.resolve());

  const readCachedValue = useCallback(() => {
    const cached = queryClient.getQueryData<EffectiveSettingsMap>(
      effectiveSettingsQueryKey({ keys: PINS_KEYS }),
    )?.[SETTING_KEYS.UI_SIDEBAR_PINS];
    return cached !== undefined ? cached.value : renderValue;
  }, [queryClient, renderValue]);

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
      const pinsQueryKey = effectiveSettingsQueryKey({ keys: PINS_KEYS });
      const revisionKey = [...pinsQueryKey, "optimistic-revision"] as const;
      const previousEntry =
        queryClient.getQueryData<EffectiveSettingsMap>(pinsQueryKey)?.[
          SETTING_KEYS.UI_SIDEBAR_PINS
        ];
      const cachedRevision = queryClient.getQueryData<number | null>(revisionKey) ?? null;
      nextSidebarPinsRevision += 1;
      const mutation = createSidebarPinsOptimisticMutation({
        currentValue: previousEntry !== undefined ? previousEntry.value : renderValue,
        currentRevision: cachedRevision,
        libraryId,
        pin,
        revision: nextSidebarPinsRevision,
      });

      queryClient.setQueryData<EffectiveSettingsMap>(pinsQueryKey, (current) => ({
        ...(current ?? {}),
        [SETTING_KEYS.UI_SIDEBAR_PINS]: {
          key: SETTING_KEYS.UI_SIDEBAR_PINS,
          ...previousEntry,
          value: mutation.optimisticValue,
          source: "profile",
        },
      }));
      queryClient.setQueryData(revisionKey, mutation.revision);
      writeChain.current = writeChain.current
        .catch(() => undefined)
        .then(() =>
          // The cache already holds every optimistic toggle applied so far, so
          // reading it here — rather than closing over this toggle's own value
          // — means a queued write sends the newest document.
          setValue.mutateAsync({
            key: SETTING_KEYS.UI_SIDEBAR_PINS,
            value: parseSidebarPins(readCachedValue()),
            identity: PROFILE_SCOPE,
          }),
        )
        .catch(() => {
          const rollback = rollbackSidebarPinsOptimisticMutation({
            currentRevision: queryClient.getQueryData<number | null>(revisionKey),
            mutation,
          });

          if (!rollback) {
            return;
          }

          queryClient.setQueryData<EffectiveSettingsMap>(pinsQueryKey, (current) => {
            if (!current) {
              return current;
            }
            if (previousEntry === undefined) {
              const next = { ...current };
              delete next[SETTING_KEYS.UI_SIDEBAR_PINS];
              return next;
            }
            return {
              ...current,
              [SETTING_KEYS.UI_SIDEBAR_PINS]: previousEntry,
            };
          });
          queryClient.setQueryData(revisionKey, rollback.revision);
        });
    },
    [queryClient, readCachedValue, renderValue, setValue],
  );

  return { togglePin, isPinned };
}
