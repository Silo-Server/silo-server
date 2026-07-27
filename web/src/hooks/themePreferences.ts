import { appearanceCache, storage } from "@/utils/storage";
import { useOptionalAuth } from "@/hooks/useAuth";
import type { ThemeId } from "@/lib/themes";
import { DEFAULT_THEME, THEME_IDS } from "@/lib/themes";

export type TextScale = "default" | "large" | "x-large";
export type TextWeight = "default" | "strong";

export interface AppearanceAuth {
  loading: boolean;
  user: { id: number } | null;
}

/**
 * The namespace that owns the device-local appearance caches, or null while
 * auth is bootstrapping or nobody is signed in.
 *
 * Appearance settings are user-scoped server side (`GET /settings` resolves
 * against `user_settings` for the authenticated user), so the user id is the
 * right owner token today. When appearance moves to profile scope — the
 * settings contract puts `ui.theme` at `profile`, and profiles on one account
 * share a user id — appending the active profile id here is the whole change:
 * every cache read and write in the app resolves its namespace through this
 * function, so no call site can be left behind.
 */
export function appearanceCacheOwner({ loading, user }: AppearanceAuth): string | null {
  return !loading && user ? String(user.id) : null;
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
