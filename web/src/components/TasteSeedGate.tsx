import type { ReactNode } from "react";
import { Navigate } from "react-router";
import { useAuth } from "@/hooks/useAuth";
import { useHasFavorites } from "@/hooks/queries/favorites";
import { useOnboardingState } from "@/hooks/queries/onboarding";
import { isTasteSeedDismissed } from "@/lib/tasteSeed";

/**
 * Redirects new profiles (no favorites yet, no skip flag) to the taste-seed
 * onboarding screen the first time they land on Home. Only checks on Home so
 * deep-links to other pages aren't blocked. Once the user picks any items
 * (or favorites anything by normal use), or explicitly skips, the gate stops
 * redirecting.
 */
export default function TasteSeedGate({ children }: { children: ReactNode }) {
  const { profile } = useAuth();
  const { data: hasFavorites, isPending, isError } = useHasFavorites();
  const onboarding = useOnboardingState({ enabled: profile !== null });

  if (isPending || isError || !profile) return <>{children}</>;

  // While the feature tour is pending (or its state unknown) the tour owns
  // the first-run moment — it ends by handing off to /taste-seed itself, so
  // redirecting now would jump the queue.
  if (onboarding.data === undefined || !onboarding.data.done) return <>{children}</>;

  const dismissed = isTasteSeedDismissed(profile.id);

  if (!hasFavorites && !dismissed) {
    return <Navigate to="/taste-seed" replace />;
  }
  return <>{children}</>;
}
