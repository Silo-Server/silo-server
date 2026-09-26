export function sanitizeAuthRedirect(value: string | null | undefined): string | null {
  if (!value) {
    return null;
  }
  if (!value.startsWith("/")) {
    return null;
  }
  if (value.startsWith("//")) {
    return null;
  }
  return value;
}

/**
 * Builds a guard redirect target (e.g. "/login") that preserves the current
 * location so the user returns to it after authenticating or choosing a
 * profile.
 */
export function guardRedirectTarget(
  base: string,
  location: { pathname: string; search: string },
): string {
  const destination = `${location.pathname}${location.search}`;
  if (destination === "/" || destination === "") {
    return base;
  }
  return `${base}?redirect=${encodeURIComponent(destination)}`;
}
