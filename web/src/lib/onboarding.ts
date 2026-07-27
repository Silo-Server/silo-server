/**
 * Client-side hints for the onboarding flow. The source of truth for tour
 * completion is the server (per profile, via /onboarding/state); these
 * localStorage flags only bridge the moments before a profile exists.
 */

const TOUR_SUPPRESSED_KEY = "onboarding_tour_suppressed";
const HOUSEHOLD_DONE_KEY = "onboarding_household_done";

/**
 * Set when an invitation was sent with show_tour=false. The onboarding gate
 * records a server-side skip for the first active profile, then clears it.
 */
export function setTourSuppressed(): void {
  try {
    localStorage.setItem(TOUR_SUPPRESSED_KEY, "true");
  } catch {
    // localStorage can throw in private browsing — worst case the tour shows.
  }
}

export function isTourSuppressed(): boolean {
  try {
    return localStorage.getItem(TOUR_SUPPRESSED_KEY) === "true";
  } catch {
    return false;
  }
}

export function clearTourSuppressed(): void {
  try {
    localStorage.removeItem(TOUR_SUPPRESSED_KEY);
  } catch {
    // Ignore.
  }
}

/**
 * Marks the household-setup step as passed for this browser so a reload
 * mid-onboarding doesn't loop back into it. Account-scoped by design: the
 * screen itself is a one-time entrance, not per-profile state.
 */
export function setHouseholdSetupDone(): void {
  try {
    localStorage.setItem(HOUSEHOLD_DONE_KEY, "true");
  } catch {
    // Ignore.
  }
}

export function isHouseholdSetupDone(): boolean {
  try {
    return localStorage.getItem(HOUSEHOLD_DONE_KEY) === "true";
  } catch {
    return true;
  }
}

export function clearHouseholdSetupDone(): void {
  try {
    localStorage.removeItem(HOUSEHOLD_DONE_KEY);
  } catch {
    // Ignore.
  }
}
