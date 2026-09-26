import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { useAuth } from "@/hooks/useAuth";
import {
  useOnboardingFlow,
  useOnboardingProgress,
  useOnboardingState,
} from "@/hooks/queries/onboarding";
import { captureProfileRequestContext } from "@/api/client";
import { clearTourSuppressed, isTourSuppressed } from "@/lib/onboarding";
import { TourHost } from "./TourHost";

/**
 * Overlays the first-run tour on Home when the active profile has never
 * completed or skipped it (server-side state, so finishing on the phone
 * silences the web and vice versa). Renders children throughout — the tour
 * is an overlay, not a route, so nothing behind it unmounts.
 */
export function OnboardingGate({ children }: { children: ReactNode }) {
  const authority = captureProfileRequestContext();
  return (
    <ProfileOnboardingGate
      key={`${authority?.serverOrigin}:${authority?.authContextVersion}:${authority?.profileId}`}
    >
      {children}
    </ProfileOnboardingGate>
  );
}
function ProfileOnboardingGate({ children }: { children: ReactNode }) {
  const authority = captureProfileRequestContext();
  const { profile } = useAuth();
  const enabled = profile !== null;
  const state = useOnboardingState({ enabled });
  const progress = useOnboardingProgress();
  // Sticky per mount: once the tour is dismissed we stop showing it even
  // before the state refetch lands.
  const [dismissed, setDismissed] = useState(false);

  const shouldShow = enabled && !dismissed && state.data !== undefined && !state.data.done;

  // An invitation sent with show_tour=false plants a local suppress hint
  // (before any profile existed). Convert it into a server-side skip for
  // this first profile, then clear it so later profiles still get the tour.
  const suppressed = shouldShow && isTourSuppressed();
  useEffect(() => {
    if (!suppressed || !state.data) return;
    progress.mutate(
      { tour_id: state.data.tour_id, skipped: true },
      {
        onSuccess: () => {
          clearTourSuppressed();
          setDismissed(true);
        },
      },
    );
    // progress is a stable mutation handle; state.data is covered by `suppressed`.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [suppressed]);

  const flow = useOnboardingFlow({ enabled: shouldShow && !suppressed });

  return (
    <>
      {children}
      {progress.isError && <div role="alert">Progress could not be saved. Reload to continue.</div>}
      {shouldShow && !suppressed && flow.data && flow.data.steps.length > 0 && (
        <TourHost
          key={`${authority?.authContextVersion}:${authority?.profileId}`}
          flow={flow.data}
          onDone={() => setDismissed(true)}
        />
      )}
    </>
  );
}
