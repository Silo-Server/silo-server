import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { OnboardingFlow, OnboardingState } from "@/api/types";

const onboardingKeys = {
  flow: () => ["onboarding", "flow"] as const,
  state: () => ["onboarding", "state"] as const,
};

export function useOnboardingState(options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: onboardingKeys.state(),
    queryFn: () => api<OnboardingState>("/onboarding/state"),
    enabled: options?.enabled ?? true,
    staleTime: 5 * 60 * 1000,
  });
}

export function useOnboardingFlow(options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: onboardingKeys.flow(),
    queryFn: () => api<OnboardingFlow>("/onboarding/flow?surface=web"),
    enabled: options?.enabled ?? true,
    staleTime: 5 * 60 * 1000,
  });
}

interface ProgressInput {
  tour_id: string;
  last_step?: string;
  completed?: boolean;
  skipped?: boolean;
}

export function useOnboardingProgress() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: ProgressInput) =>
      api("/onboarding/progress", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: (_data, variables) => {
      if (variables.completed || variables.skipped) {
        queryClient.invalidateQueries({ queryKey: onboardingKeys.state() });
      }
    },
  });
}
