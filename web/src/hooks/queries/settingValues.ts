import { useCallback, useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { isProfileRequestContextCurrent, type ProfileRequestContextSnapshot } from "@/api/client";
import { v2, V2ProblemError, type V2Query, type V2Result } from "@/api/v2/request";
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

type EffectiveSettingV2 = V2Result<"GET /api/v2/settings/values/effective">["items"][number];

/**
 * One resolved setting as the v2 contract returns it, with the key narrowed
 * to the vendored manifest and the source/scope narrowed to the ladder this
 * client knows. The contract leaves those as open strings because legacy
 * stored rows can hold values outside the enum; the narrowing happens once,
 * in the query function, so every screen reads the typed shape.
 */
export type EffectiveSetting<T = unknown> = Omit<
  EffectiveSettingV2,
  "key" | "value" | "stored_value" | "source" | "scope" | "library_id" | "definition_revision"
> & {
  key: SettingKey;
  value: T;
  /** Present only when policy narrowed the answer; this is what the user chose. */
  stored_value?: T;
  source: SettingSource;
  /** The scope holding the value, so a reset can target it exactly. */
  scope?: SettingScope;
  /** The contract renders ids as strings; the app addresses libraries by number. */
  library_id?: number;
  definition_revision?: number;
};

/** The cache shape one effective-settings query resolves to. */
export type EffectiveSettingsMap = Partial<Record<SettingKey, EffectiveSetting>>;

const SETTING_SCOPES: readonly SettingScope[] = [
  "account",
  "profile",
  "profile_device",
  "profile_client",
  "profile_library",
  "profile_series",
];
const KNOWN_SETTING_KEYS = new Set<string>(Object.values(SETTING_KEYS));

function isSettingScope(value: string | undefined): value is SettingScope {
  return value !== undefined && (SETTING_SCOPES as readonly string[]).includes(value);
}

/**
 * Narrow one v2 effective-setting item onto the client's closed vocabularies.
 * The contract leaves key, source and scope as open strings (a newer server may
 * add values); an item this client does not know is dropped rather than
 * asserted, so callers never see a key outside SETTING_KEYS.
 */
function effectiveSettingFromV2(item: EffectiveSettingV2): EffectiveSetting | undefined {
  if (!KNOWN_SETTING_KEYS.has(item.key)) return undefined;
  const source: SettingSource | undefined =
    item.source === "default" ? "default" : isSettingScope(item.source) ? item.source : undefined;
  if (source === undefined) return undefined;
  return {
    ...item,
    key: item.key as SettingKey,
    source,
    scope: isSettingScope(item.scope) ? item.scope : undefined,
    library_id: item.library_id === undefined ? undefined : Number(item.library_id),
  };
}

type SettingIdentityQuery = V2Query<"PUT /api/v2/settings/values/{key}">;

function identityQuery(identity: SettingIdentity): SettingIdentityQuery {
  return {
    scope: identity.scope,
    library_id: identity.libraryId === undefined ? undefined : String(identity.libraryId),
    series_id: identity.seriesId,
    device_id: identity.deviceId,
    profile_id: identity.profileId,
  };
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
      const result = await v2("GET /api/v2/settings/values/effective", {
        query: {
          keys: keys?.length ? [...keys] : undefined,
          library_ids: libraryIds?.length ? libraryIds.map(String) : undefined,
          series_ids: seriesIds?.length ? [...seriesIds] : undefined,
          device_id: deviceId || undefined,
          profile_id: profileId || undefined,
        },
      });
      const byKey: EffectiveSettingsMap = {};
      for (const item of result.items) {
        const setting = effectiveSettingFromV2(item);
        if (setting) byKey[setting.key] = setting;
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

/** The HTTP status of a documented v2 problem, or null for anything else (transport, network). */
export function settingMutationStatus(error: unknown): number | null {
  return error instanceof V2ProblemError ? error.status : null;
}

export function isDefinitiveSettingMutationRejection(error: unknown): boolean {
  // Ordinary 4xx responses reject the request before applying it. A 408 or
  // 5xx can be emitted by the server or a gateway after the mutation reached
  // the handler, so those remain ambiguous and require reconciliation.
  const status = settingMutationStatus(error);
  return status !== null && status >= 400 && status < 500 && status !== 408;
}

/**
 * Whether a write or clear failed because nothing is stored at that scope.
 * For a reset that is the requested state, not a failure.
 */
export function isSettingValueMissing(error: unknown): boolean {
  return settingMutationStatus(error) === 404;
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
    }: {
      key: SettingKey;
      value: unknown;
      identity: SettingIdentity;
      /**
       * Stable across retries of one desired-state write. The v2 contract
       * declares no idempotency header (a repeated PUT converges on the same
       * value), so the id only identifies the intent to the queue that owns it.
       */
      mutationId?: string;
      /**
       * Whole-document editors may serialize several optimistic writes and
       * invalidate once their queue drains. Intermediate refetches would
       * otherwise replace the newest optimistic document with an older server
       * value. Ordinary callers should leave this enabled.
       */
      invalidateOnSettled?: boolean;
    }) =>
      v2("PUT /api/v2/settings/values/{key}", {
        path: { key },
        query: identityQuery(identity),
        body: { value },
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
      profileAuth,
      invalidateOnSettled,
    }: {
      item: ShortcutTarget;
      present: boolean;
      /** Stable across retries of this desired-state operation; see useSetSettingValue. */
      mutationId: string;
      /** Profile id and matching PIN token captured with this intent. */
      profileAuth: ProfileAuthSnapshot;
      /** A local serialized editor can defer refetching until its queue drains. */
      invalidateOnSettled?: boolean;
    }) => {
      try {
        return await v2("PUT /api/v2/settings/values/nav.shortcuts/item", {
          // The contract types the item as an open object; the shortcut's
          // members are fixed by the nav.shortcuts definition it lands in.
          body: { item: { ...item }, present },
          profileContext: profileAuth,
        });
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
      v2("DELETE /api/v2/settings/values/{key}", { path: { key }, query: identityQuery(identity) }),
    onSuccess: (_data, variables) => {
      return invalidateSettingValueQueries(qc, variables.identity);
    },
    onError: (error, variables) => {
      // DELETE is idempotent for reset callers: a 404 means another client
      // already cleared the value, so stale effective caches must catch up.
      if (shouldReconcileAfterMutationError(error) || isSettingValueMissing(error)) {
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
export type SettingsCapabilities = Pick<
  V2Result<"GET /api/v2/settings/contract/capabilities">,
  "api_version" | "revision" | "contract_etag"
> &
  Partial<
    Pick<
      V2Result<"GET /api/v2/settings/contract/capabilities">,
      "supports_batched_effective" | "supports_idempotent_writes" | "supports_atomic_shortcuts"
    >
  >;

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
    queryFn: (): Promise<SettingsCapabilities> => v2("GET /api/v2/settings/contract/capabilities"),
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
