import { appearanceCache, storage } from "@/utils/storage";
import type { ThemeId } from "@/lib/themes";
import { DEFAULT_THEME, THEME_IDS } from "@/lib/themes";

export type TextScale = "default" | "large" | "x-large";
export type TextWeight = "default" | "strong";

export interface AppearanceAuth {
  loading: boolean;
  user: { id: number } | null;
}

/**
 * The account that owns the device-local appearance caches, or null while auth
 * is bootstrapping or nobody is signed in.
 *
 * Appearance settings are user-scoped server side (`GET /settings` resolves
 * against `user_settings` for the authenticated user), so the user id is the
 * right owner token today. Widen this one function if appearance ever moves to
 * profile scope — profiles on one account share a user id.
 */
export function appearanceCacheOwner({ loading, user }: AppearanceAuth): string | null {
  return !loading && user ? String(user.id) : null;
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
