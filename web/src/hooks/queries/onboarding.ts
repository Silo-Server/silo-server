import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { v2, type V2Body, type V2Result } from "@/api/v2/request";

/** The server-driven tour manifest for this surface. */
export type OnboardingFlow = V2Result<"GET /api/v2/onboarding/flow">;
/** One step of the tour manifest. */
export type OnboardingStep = OnboardingFlow["steps"][number];
/** A store or documentation link a step renders. */
export type OnboardingStepLink = OnboardingStep["links"][number];
/** The profile's progress through the current tour. */
export type OnboardingState = V2Result<"GET /api/v2/onboarding/state">;

const onboardingKeys = {
  flow: () => ["onboarding", "flow"] as const,
  state: () => ["onboarding", "state"] as const,
};

export function useOnboardingState(options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: onboardingKeys.state(),
    queryFn: () => v2("GET /api/v2/onboarding/state"),
    enabled: options?.enabled ?? true,
    staleTime: 5 * 60 * 1000,
  });
}

export function useOnboardingFlow(options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: onboardingKeys.flow(),
    queryFn: () => v2("GET /api/v2/onboarding/flow", { query: { surface: "web" } }),
    enabled: options?.enabled ?? true,
    staleTime: 5 * 60 * 1000,
  });
}

type ProgressInput = V2Body<"POST /api/v2/onboarding/progress">;

export function useOnboardingProgress() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: ProgressInput) => v2("POST /api/v2/onboarding/progress", { body }),
    onSuccess: (_data, variables) => {
      if (variables.completed || variables.skipped) {
        queryClient.invalidateQueries({ queryKey: onboardingKeys.state() });
      }
    },
  });
}
