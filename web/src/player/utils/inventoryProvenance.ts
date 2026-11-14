/**
 * Whether a plan's track inventory is still provisional.
 *
 * The plan publishes `inventory_status` ("declared" until a probe persists,
 * then "verified") and, for a virtual source, `inventory_provenance` from the
 * resolve that produced it ("declared", "pending", "verified", or "failed").
 * The status is stamp-derived, so a stale stamp can read "verified" while the
 * resolve actually served provider-declared metadata; provenance is
 * authoritative when present.
 *
 * The only confident state is positive probe evidence: an explicit "verified"
 * provenance, or no provenance plus a status that is not "declared". A failed
 * probe serves declared metadata too, so it stays provisional. Missing fields
 * (older servers, non-virtual sources) keep the previous confident behavior.
 */
/**
 * Reads the deferred-track-inventory state of a plan into one place.
 *
 * `tracks_pending` is the explicit signal: the server handed back a
 * first-byte URL with only the minimal recipe and the full enumeration is
 * still running. Older servers never send it, so the pair
 * (status, provenance) is the fallback — any non-verified provenance means
 * the list is still declared metadata. The only confident state is positive
 * probe evidence. Missing fields (older servers, non-virtual sources) keep
 * the previous confident behavior.
 */
export function isInventoryProvisional(
  status?: string | null,
  provenance?: string | null,
  tracksPending?: boolean | null,
): boolean {
  if (tracksPending === true) return true;
  if (provenance === "verified") return false;
  if (provenance === "declared" || provenance === "pending" || provenance === "failed") {
    return true;
  }
  return status === "declared";
}

/**
 * Whether the player overlay's track menu has no rows to show yet.
 *
 * A row the plan or the selected version already declares is inventory: the
 * menu renders it immediately and marks it unverified, then swaps in probe
 * evidence when it lands. Only an empty list waits in discovery, so a declared
 * track list never renders as "Discovering"/"Unavailable" and an empty verified
 * list keeps its final empty state.
 *
 * `planProvisional` is the plan's own verdict from `isInventoryProvisional`.
 */
export function isOverlayInventoryProvisional(planProvisional: boolean, rowCount: number): boolean {
  if (rowCount > 0) return false;
  return planProvisional;
}

/**
 * How long a provisional menu may spin before it stops implying progress and
 * offers an explicit retry. A deferred probe usually lands in seconds; a menu
 * that is still provisional after this long is a stuck discovery the viewer
 * must be able to act on rather than watch forever.
 */
export const PROVISIONAL_INVENTORY_TIMEOUT_MS = 15_000;

/**
 * Whether a provisional menu has outlived the bound at `now`.
 *
 * `provisionalSince` is the moment the current provisional stretch began, or
 * null when the menu is not provisional. Callers that render a spinner reset
 * it when the stretch ends or the viewer retries.
 */
export function isProvisionalInventoryExpired(
  provisional: boolean,
  provisionalSince: number | null,
  now: number,
  timeoutMs: number = PROVISIONAL_INVENTORY_TIMEOUT_MS,
): boolean {
  if (!provisional || provisionalSince === null) return false;
  return now - provisionalSince >= timeoutMs;
}

/**
 * Whether a plan's subtitle inventory is provisional, including the explicit
 * `inventory_unavailable` answer. An empty inventory the server has not
 * established carries that reason even when the stamp and provenance look
 * confident; the menu must stay pending rather than present the empty list as
 * final. `isInventoryProvisional` alone cannot see it.
 */
export function isPlanSubtitleInventoryProvisional(plan: {
  inventory_status?: string | null;
  inventory_provenance?: string | null;
  tracks_pending?: boolean | null;
  claims?: { subtitles?: { reason?: string } | null } | null;
}): boolean {
  if (plan.claims?.subtitles?.reason === "inventory_unavailable") return true;
  return isInventoryProvisional(
    plan.inventory_status,
    plan.inventory_provenance,
    plan.tracks_pending,
  );
}
