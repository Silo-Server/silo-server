import { useCallback, useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  api,
  ApiClientError,
  apiWithProfileRequestContext,
  isProfileRequestContextCurrent,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { storage } from "@/utils/storage";
import {
  SETTING_DEFINITIONS,
  SETTING_KEYS,
  SETTINGS_API_VERSION,
  type SettingKey,
} from "@/lib/settingsContract";
import { useEventChannel } from "@/components/realtimeEventsContext";
import type { ShortcutTarget } from "@/lib/uiCustomization";
import { deviceKeys, settingsKeys } from "./keys";

/**
 * Typed access to the canonical settings API.
 *
 * The hooks in ./settings.ts speak the legacy string-only endpoints: every
 * value is a string, scope is implied by which function you call, and an
 * unknown key is silently accepted. These speak the contract instead — values
 * are typed JSON, scope is explicit, and a key that is not in the manifest
 * cannot be expressed because SettingKey is generated from it.
 */

/** The remote scopes a value can live at. */
export type SettingScope =
  | "account"
  | "profile"
  | "profile_device"
  | "profile_client"
  | "profile_library"
  | "profile_series";

/** Where a resolved value came from, or "default" when nothing was stored. */
export type SettingSource = SettingScope | "default";

export interface SettingIdentity {
  scope: SettingScope;
  /** Required for profile_library. */
  libraryId?: number;
  /** Required for profile_series. */
  seriesId?: string;
  /**
   * A device other than the one this browser is. Omit to address the current
   * device, which is what every screen except "your devices" wants.
   */
  deviceId?: string;
  /**
   * A profile other than the signed-in one. Only the household parent may set
   * this; the server answers 403 for anyone else.
   */
  profileId?: string;
}

/**
 * Profile authorization captured as one synchronous snapshot when an intent is
 * created. A queued write must not combine one profile id with another
 * profile's PIN token after the active profile changes.
 */
export type ProfileAuthSnapshot = ProfileRequestContextSnapshot;

export interface EffectiveSetting<T = unknown> {
  key: SettingKey;
  value: T;
  source: SettingSource;
  /** Present only when policy narrowed the answer; this is what the user chose. */
  stored_value?: T;
  constrained?: boolean;
  constraint_kind?: "ceiling" | "floor" | "allowlist" | "locked";
  /** Advisory values: contract floor, deployment-observed tags, and current value. */
  suggested_values?: string[];
  /** The scope holding the value, so a reset can target it exactly. */
  scope?: SettingScope;
  client_family?: "tv" | "mobile" | "tablet" | "desktop" | "web";
  library_id?: number;
  series_id?: string;
}

interface EffectiveResponse {
  settings: EffectiveSetting[];
  revision: number;
}

/** The cache shape one effective-settings query resolves to. */
export type EffectiveSettingsMap = Partial<Record<SettingKey, EffectiveSetting>>;

function identityQuery(identity: SettingIdentity): string {
  const params = new URLSearchParams({ scope: identity.scope });
  if (identity.libraryId !== undefined) params.set("library_id", String(identity.libraryId));
  if (identity.seriesId !== undefined) params.set("series_id", identity.seriesId);
  if (identity.deviceId !== undefined) params.set("device_id", identity.deviceId);
  if (identity.profileId !== undefined) params.set("profile_id", identity.profileId);
  return params.toString();
}

function activeProfileId() {
  return storage.get(storage.KEYS.PROFILE_ID);
}

/**
 * The cache key one useEffectiveSettings call resolves under. Exported so a
 * store that layers optimistic updates on top of an effective read (sidebar
 * pins) can target the exact entry that read populated.
 */
export function effectiveSettingsQueryKey(options?: {
  keys?: readonly SettingKey[];
  libraryIds?: readonly number[];
  seriesIds?: readonly string[];
  deviceId?: string;
  profileId?: string;
}) {
  const { keys, libraryIds, seriesIds, deviceId, profileId } = options ?? {};
  return [
    ...settingsKeys.all,
    "values",
    "effective",
    // The device and profile a read resolved for are part of the identity of
    // the answer, not just of the request. Without them a read of the Apple
    // TV's values would land on the same cache entry as this browser's and
    // serve one device's settings as another's.
    profileId ?? activeProfileId(),
    deviceId ?? "",
    keys ? [...keys].sort().join(",") : "*",
    libraryIds ? [...libraryIds].sort().join(",") : "",
    seriesIds ? [...seriesIds].sort().join(",") : "",
  ] as const;
}

/**
 * Resolve settings the way the server does, including the scope each answer
 * came from.
 *
 * Batched on purpose: a settings screen wants every key at once and a series
 * view wants several keys for one series, and the server answers either in one
 * read. Passing no keys returns every remote setting.
 */
export function useEffectiveSettings(options?: {
  keys?: readonly SettingKey[];
  libraryIds?: readonly number[];
  seriesIds?: readonly string[];
  /** Resolve for a device other than this browser. */
  deviceId?: string;
  /** Resolve for another profile on the account (household parent only). */
  profileId?: string;
  enabled?: boolean;
}) {
  const keys = options?.keys;
  const libraryIds = options?.libraryIds;
  const seriesIds = options?.seriesIds;
  const deviceId = options?.deviceId;
  const profileId = options?.profileId;

  return useQuery({
    queryKey: effectiveSettingsQueryKey({ keys, libraryIds, seriesIds, deviceId, profileId }),
    queryFn: async () => {
      const params = new URLSearchParams();
      if (keys?.length) params.set("keys", keys.join(","));
      if (libraryIds?.length) params.set("library_ids", libraryIds.join(","));
      if (seriesIds?.length) params.set("series_ids", seriesIds.join(","));
      if (deviceId) params.set("device_id", deviceId);
      if (profileId) params.set("profile_id", profileId);
      const query = params.toString();
      const result = await api<EffectiveResponse>(
        `/settings/values/effective${query ? `?${query}` : ""}`,
      );
      const byKey: EffectiveSettingsMap = {};
      for (const setting of result.settings) {
        byKey[setting.key] = setting;
      }
      return byKey;
    },
    enabled: options?.enabled ?? true,
    staleTime: 5 * 60 * 1000,
  });
}

/**
 * The effective value for one key, already unwrapped, falling back to the
 * contract default while the request is in flight.
 *
 * The default comes from the generated table rather than a literal at the call
 * site, which is what stops a client and the server disagreeing about what
 * "unset" means — the bug that made every default-on toggle flip off against a
 * server that did not know the key.
 */
export function useSettingValue<T = unknown>(
  key: SettingKey,
  options?: { libraryIds?: readonly number[]; seriesIds?: readonly string[]; enabled?: boolean },
) {
  const query = useEffectiveSettings({ keys: [key], ...options });
  const setting = query.data?.[key];
  return {
    ...query,
    value: (setting?.value ?? SETTING_DEFINITIONS[key].defaultValue) as T,
    source: setting?.source ?? "default",
    constrained: setting?.constrained ?? false,
    setting,
  };
}

export function isDefinitiveSettingMutationRejection(error: unknown): boolean {
  // Ordinary 4xx responses reject the request before applying it. A 408 or
  // 5xx can be emitted by the server or a gateway after the mutation reached
  // the handler, so those remain ambiguous and require reconciliation.
  return (
    error instanceof ApiClientError &&
    error.status >= 400 &&
    error.status < 500 &&
    error.status !== 408
  );
}

function shouldReconcileAfterMutationError(error: unknown): boolean {
  return !isDefinitiveSettingMutationRejection(error);
}

function invalidateSettingValueQueries(
  queryClient: ReturnType<typeof useQueryClient>,
  identity: SettingIdentity,
) {
  const invalidations = [
    queryClient.invalidateQueries({ queryKey: [...settingsKeys.all, "values"] }),
  ];
  // A device-scoped write changes that device's "how many things differ"
  // count, which the device list shows. Without this the badge stays stale
  // until the list's own staleTime expires.
  if (identity.scope === "profile_device") {
    invalidations.push(queryClient.invalidateQueries({ queryKey: deviceKeys.all }));
  }
  return Promise.all(invalidations).then(() => undefined);
}

/** Write one value at one scope. */
export function useSetSettingValue() {
  const qc = useQueryClient();

  return useMutation({
    mutationFn: ({
      key,
      value,
      identity,
      mutationId,
    }: {
      key: SettingKey;
      value: unknown;
      identity: SettingIdentity;
      /** Optional idempotency key; a retry with the same id replays rather than re-applying. */
      mutationId?: string;
      /**
       * Whole-document editors may serialize several optimistic writes and
       * invalidate once their queue drains. Intermediate refetches would
       * otherwise replace the newest optimistic document with an older server
       * value. Ordinary callers should leave this enabled.
       */
      invalidateOnSettled?: boolean;
    }) =>
      api(`/settings/values/${key}?${identityQuery(identity)}`, {
        method: "PUT",
        headers: {
          "Content-Type": "application/json",
          ...(mutationId ? { "X-Silo-Mutation-Id": mutationId } : {}),
        },
        body: JSON.stringify({ value }),
      }),
    onSuccess: (_data, variables) => {
      if (variables.invalidateOnSettled === false) return;
      // Keep ordinary controls pending until their active effective-value
      // reads reconcile. Otherwise a rapid follow-up edit can spread a stale
      // object and silently undo the first field that was just saved.
      return invalidateSettingValueQueries(qc, variables.identity);
    },
    onError: (error, variables) => {
      if (variables.invalidateOnSettled === false) return;
      if (shouldReconcileAfterMutationError(error)) {
        return invalidateSettingValueQueries(qc, variables.identity);
      }
    },
  });
}

/**
 * Ensure one profile-wide navigation shortcut is present or absent.
 *
 * This semantic endpoint is intentionally separate from useSetSettingValue:
 * nav.shortcuts is shared by every client family, so replacing its whole
 * document can erase a shortcut another device added from the same base.
 */
export function useSetNavigationShortcutPresence() {
  const qc = useQueryClient();

  const mutateAsync = useCallback(
    async ({
      item,
      present,
      mutationId,
      profileAuth,
      invalidateOnSettled,
    }: {
      item: ShortcutTarget;
      present: boolean;
      /** Stable across retries of this desired-state operation. */
      mutationId: string;
      /** Profile id and matching PIN token captured with this intent. */
      profileAuth: ProfileAuthSnapshot;
      /** A local serialized editor can defer refetching until its queue drains. */
      invalidateOnSettled?: boolean;
    }) => {
      try {
        return await apiWithProfileRequestContext(
          `/settings/values/nav.shortcuts/item`,
          profileAuth,
          {
            method: "PUT",
            headers: {
              "Content-Type": "application/json",
              "X-Silo-Mutation-Id": mutationId,
            },
            body: JSON.stringify({ item, present }),
          },
        );
      } finally {
        if (invalidateOnSettled !== false && isProfileRequestContextCurrent(profileAuth)) {
          void qc.invalidateQueries({ queryKey: [...settingsKeys.all, "values"] });
        }
      }
    },
    [qc],
  );

  // This deliberately is not a TanStack mutation. Mutation variables remain
  // in the mutation cache after settlement; the PIN token should live only in
  // the in-memory serialized queue/request closure that needs it.
  return { mutateAsync };
}

/** Clear the value at one scope, so the setting inherits again. */
export function useClearSettingValue() {
  const qc = useQueryClient();

  return useMutation({
    mutationFn: ({ key, identity }: { key: SettingKey; identity: SettingIdentity }) =>
      api(`/settings/values/${key}?${identityQuery(identity)}`, { method: "DELETE" }),
    onSuccess: (_data, variables) => {
      return invalidateSettingValueQueries(qc, variables.identity);
    },
    onError: (error, variables) => {
      // DELETE is idempotent for reset callers: a 404 means another client
      // already cleared the value, so stale effective caches must catch up.
      if (
        shouldReconcileAfterMutationError(error) ||
        (error instanceof ApiClientError && error.status === 404)
      ) {
        return invalidateSettingValueQueries(qc, variables.identity);
      }
    },
  });
}

/**
 * The server's contract revision, for the server-upgrade-required case: a
 * client built against a newer manifest hides definitions the connected server
 * does not know rather than offering a choice it will refuse.
 */
export interface SettingsCapabilities {
  api_version: number;
  revision: number;
  contract_etag: string;
  /** Effective reads can resolve the provider's key batch atomically. */
  supports_batched_effective?: boolean;
  /** Persisted/retried writes can replay one mutation id without re-applying. */
  supports_idempotent_writes?: boolean;
  /** Added alongside the semantic nav.shortcuts item endpoint. */
  supports_atomic_shortcuts?: boolean;
}

/** Whether this server can safely read and write one vendored definition. */
export function settingsCapabilitiesSupportKey(
  capabilities: SettingsCapabilities | undefined,
  key: SettingKey,
) {
  return (
    capabilities?.api_version === SETTINGS_API_VERSION &&
    capabilities.revision >= SETTING_DEFINITIONS[key].introducedIn &&
    capabilities.supports_batched_effective === true &&
    capabilities.supports_idempotent_writes === true
  );
}

/**
 * Atomic shortcut mutations need both the revision-5 definition and the
 * semantic endpoint capability. Missing flags from older servers fail closed.
 */
export function settingsCapabilitiesSupportAtomicShortcuts(
  capabilities: SettingsCapabilities | undefined,
) {
  return (
    settingsCapabilitiesSupportKey(capabilities, SETTING_KEYS.NAV_SHORTCUTS) &&
    capabilities?.supports_atomic_shortcuts === true
  );
}

export function useSettingsCapabilities() {
  return useQuery({
    queryKey: [...settingsKeys.all, "capabilities"] as const,
    queryFn: () => api<SettingsCapabilities>("/settings/contract/capabilities"),
    staleTime: 30 * 60 * 1000,
  });
}

/** The user_settings.changed payload. Identity only — never a value. */
interface UserSettingsChangedPayload {
  key?: string;
  scope?: SettingScope;
  profile_id?: string;
}

/**
 * Keeps this client's resolved settings honest while another device edits them.
 *
 * The server publishes user_settings.changed on every canonical write and
 * delete, carrying only what changed and never the value — admins receive
 * other accounts' events, so a value in the payload would leak private
 * settings. The event is therefore a pure invalidation signal: mark the value
 * queries stale and let react-query refetch the ones a mounted screen is
 * actually reading. Nothing is written into the cache from the socket.
 *
 * A burst (a settings screen saving several keys, or a profile sync writing a
 * batch) costs one refetch, not one per event: invalidateQueries only marks
 * the entries stale and react-query coalesces the resulting fetches per key.
 */
export function useSettingValuesRealtime() {
  const qc = useQueryClient();

  const handlers = useMemo(
    () => ({
      onEvent: (message: unknown) => {
        const event = message as { event?: string; data?: UserSettingsChangedPayload };
        if (event?.event !== "user_settings.changed") return;

        // One account can have several profiles, and admins additionally
        // receive other accounts' user-scoped events (the frame carries no
        // user id to filter on, so that case falls through to a harmless
        // refetch). A profile-addressed change to a profile that is not the
        // signed-in one cannot alter what we resolve, so it is dropped. An
        // account-scoped change carries no profile and does affect us.
        const changedProfile = event.data?.profile_id;
        const activeProfile = activeProfileId();
        if (changedProfile && activeProfile && changedProfile !== activeProfile) return;

        qc.invalidateQueries({ queryKey: [...settingsKeys.all, "values"] });
      },
    }),
    [qc],
  );

  useEventChannel("user_settings", handlers);
}
