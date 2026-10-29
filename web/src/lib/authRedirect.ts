// eslint-disable-next-line no-control-regex -- the pattern matches control characters on purpose
const CONTROL_CHARACTER = /[\u0000-\u001f\u007f]/;

/**
 * The page to open after signing in or choosing a profile, or null when the
 * value could leave this site. Browsers read a backslash in the path as a
 * slash and drop tabs and newlines, so "/\evil.example" and "/\t/evil.example"
 * both resolve to another host. The query and fragment can't change the host.
 */
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
  const path = value.split(/[?#]/, 1)[0] ?? "";
  if (path.includes("\\") || CONTROL_CHARACTER.test(path)) {
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
