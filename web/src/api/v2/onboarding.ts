import {
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { v2, type V2Body, type V2Result } from "./request";

export type OnboardingProgressInput = V2Body<"PUT /api/v2/onboarding/progress">;
export function requireOnboardingAuthority(context: ProfileRequestContextSnapshot) {
  if (!isCapturedProfileAuthorityActive(context)) throw new StaleApiRequestContextError();
}
export async function readOnboardingState(context: ProfileRequestContextSnapshot) {
  requireOnboardingAuthority(context);
  let etag = "";
  const state = await v2("GET /api/v2/onboarding/state", {
    profileContext: context,
    onResponse: (r) => {
      etag = r.headers.get("ETag") ?? "";
    },
  });
  requireOnboardingAuthority(context);
  if (!etag) throw new Error("Onboarding state has no revision. Reload to continue.");
  return { state, etag };
}
export async function readOnboardingFlow(context: ProfileRequestContextSnapshot) {
  requireOnboardingAuthority(context);
  const flow = await v2("GET /api/v2/onboarding/flow", {
    profileContext: context,
    query: { surface: "web" },
  });
  requireOnboardingAuthority(context);
  return flow;
}

/** One mounted tour advances only from acknowledged revisions. A failed or
 * uncertain write stops the sequence; reloading starts from canonical state. */
export function createOnboardingWriter(context: ProfileRequestContextSnapshot) {
  let etag: string | undefined;
  let inFlight = false;
  let stopped = false;
  return async (body: OnboardingProgressInput) => {
    requireOnboardingAuthority(context);
    if (stopped) throw new Error("Reload the tour before continuing.");
    if (inFlight) throw new Error("Onboarding progress is still being saved.");
    inFlight = true;
    try {
      etag ??= (await readOnboardingState(context)).etag;
      let nextTag = "";
      const state = await v2("PUT /api/v2/onboarding/progress", {
        body,
        profileContext: context,
        retryAuthentication: false,
        headers: { "If-Match": etag },
        onResponse: (r) => {
          nextTag = r.headers.get("ETag") ?? "";
        },
      });
      requireOnboardingAuthority(context);
      if (!nextTag) throw new Error("Onboarding progress has no revision. Reload to continue.");
      etag = nextTag;
      return state;
    } catch (error) {
      stopped = true;
      throw error;
    } finally {
      inFlight = false;
    }
  };
}

export type OnboardingFlow = V2Result<"GET /api/v2/onboarding/flow">;
export type OnboardingStep = OnboardingFlow["steps"][number];
export type OnboardingStepLink = NonNullable<OnboardingStep["links"]>[number];
