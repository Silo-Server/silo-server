/**
 * The one-time token of a password reset or invitation link.
 *
 * Links carry it in the fragment (`/reset-password#<token>`), which a browser
 * never sends to the server or in a Referer header. Links issued before that
 * carry it in the path (`/reset-password/<token>`). Either way it is taken out
 * of the address before the app reads the URL, and kept in the history
 * entry's state, where a reload or a Back still finds it. The address bar,
 * the router and everything keyed by URL then see the bare page path.
 */

const STATE_KEY = "siloLinkToken";
const LINK_PAGES = ["/reset-password", "/invite"];

function tokenInAddress(): { page: string; token: string } | null {
  const { pathname, hash } = window.location;
  for (const page of LINK_PAGES) {
    if (pathname.startsWith(`${page}/`)) {
      // A path token wins over a fragment: the path form has no use for one.
      const token = pathname.slice(page.length + 1).split("/")[0];
      if (token) return { page, token };
    }
    if ((pathname === page || pathname === `${page}/`) && hash.length > 1) {
      return { page, token: hash.slice(1) };
    }
  }
  return null;
}

/**
 * Moves a link token from the address into the current history entry's
 * state. Returns whether it moved one.
 */
export function moveLinkTokenOutOfAddress(): boolean {
  const found = tokenInAddress();
  if (!found) return false;
  const state = (window.history.state ?? {}) as Record<string, unknown>;
  window.history.replaceState({ ...state, [STATE_KEY]: found.token }, "", found.page);
  return true;
}

/**
 * Takes link tokens out of the address for the life of the page. Call it
 * before the first render.
 */
export function installLinkTokenHandling(): void {
  moveLinkTokenOutOfAddress();
  // A link pasted over a link page in the same tab changes only the
  // fragment: nothing loads again and the router is not told. Take that
  // token too, then load the page afresh for it.
  window.addEventListener("hashchange", () => {
    if (moveLinkTokenOutOfAddress()) window.location.reload();
  });
}

/** The token the current history entry holds; "" when it holds none. */
export function linkToken(): string {
  const token = (window.history.state as Record<string, unknown> | null)?.[STATE_KEY];
  return typeof token === "string" ? token : "";
}
