import { appearanceCache, storage } from "@/utils/storage";
import { useOptionalAuth } from "@/hooks/useAuth";
import type { ThemeId } from "@/lib/themes";
import { DEFAULT_THEME, THEME_IDS } from "@/lib/themes";

export type TextScale = "default" | "large" | "x-large";
export type TextWeight = "default" | "strong";

export interface AppearanceAuth {
  loading: boolean;
  user: { id: number } | null;
  profile: { id: string } | null;
}

/**
 * The namespace that owns the device-local appearance caches, or null while
 * auth is bootstrapping, nobody is signed in, or no profile has been selected
 * yet.
 *
 * Appearance settings are profile-scoped in the settings contract (`ui.theme`
 * lives at `profile`, with an optional `profile_device` override), and several
 * profiles on one account share a user id — so the owner token is the user id
 * plus the active profile id. Every cache read and write in the app resolves
 * its namespace through this function, so no call site can be left behind.
 *
 * On the profile picker (user signed in, no profile chosen) the owner is null,
 * which falls back to the last profile that wrote here — the same "keep the
 * last look" behavior as the login screen — and gates off the settings request,
 * which cannot resolve profile scope without an active profile anyway.
 */
export function appearanceCacheOwner({ loading, user, profile }: AppearanceAuth): string | null {
  return !loading && user && profile ? `${user.id}:${profile.id}` : null;
}

/**
 * `appearanceCacheOwner` for the currently authenticated session.
 *
 * The three appearance providers each need the same owner token, and each was
 * adapting the auth context to `AppearanceAuth` itself with identical code.
 * That put the shape of auth back in three places, which is the duplication the
 * doc comment above argues against: widening ownership has to be a change to
 * this file alone.
 */
export function useAppearanceCacheOwner(): string | null {
  const auth = useOptionalAuth();
  return appearanceCacheOwner({
    loading: auth?.loading ?? false,
    user: auth?.user ? { id: auth.user.id } : null,
    profile: auth?.profile ? { id: auth.profile.id } : null,
  });
}

export function isValidTheme(value: string | null | undefined): value is ThemeId {
  return typeof value === "string" && (THEME_IDS as readonly string[]).includes(value);
}

export function parseTextScale(value: string | null | undefined): TextScale {
  return value === "large" || value === "x-large" ? value : "default";
}

export function parseTextWeight(value: string | null | undefined): TextWeight {
  return value === "strong" ? "strong" : "default";
}

export function parseHighContrast(value: string | null | undefined): boolean {
  return value === "true";
}

export function getInitialTheme(owner: string | null): ThemeId {
  const stored = appearanceCache.get(storage.KEYS.THEME, owner);
  return isValidTheme(stored) ? stored : DEFAULT_THEME;
}
