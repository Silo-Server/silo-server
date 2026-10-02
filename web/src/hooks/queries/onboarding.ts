import { useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { captureProfileRequestContext, StaleApiRequestContextError } from "@/api/client";
import {
  createOnboardingWriter,
  readOnboardingFlow,
  readOnboardingState,
  requireOnboardingAuthority,
  type OnboardingProgressInput,
} from "@/api/v2/onboarding";

function useOnboardingContext() {
  const context = captureProfileRequestContext();
  const scope = [
    "onboarding",
    context?.serverOrigin,
    context?.authContextVersion,
    context?.profileId,
    context?.profileToken,
  ] as const;
  return { context, scope };
}
export function useOnboardingState(options?: { enabled?: boolean }) {
  const { context, scope } = useOnboardingContext();
  return useQuery({
    queryKey: [...scope, "state"],
    queryFn: async () => {
      if (!context) throw new StaleApiRequestContextError();
      return (await readOnboardingState(context)).state;
    },
    enabled: !!context && (options?.enabled ?? true),
    staleTime: 5 * 60 * 1000,
  });
}
export function useOnboardingFlow(options?: { enabled?: boolean }) {
  const { context, scope } = useOnboardingContext();
  return useQuery({
    queryKey: [...scope, "flow"],
    queryFn: () => {
      if (!context) throw new StaleApiRequestContextError();
      return readOnboardingFlow(context);
    },
    enabled: !!context && (options?.enabled ?? true),
    staleTime: 5 * 60 * 1000,
  });
}
export function useOnboardingProgress() {
  const queryClient = useQueryClient();
  const { context, scope } = useOnboardingContext();
  const writer = useMemo(
    () => (context ? createOnboardingWriter(context) : null),
    // Token refresh preserves authority; account/profile changes start a new sequence.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [context?.serverOrigin, context?.authContextVersion, context?.profileId, context?.profileToken],
  );
  return useMutation({
    retry: false,
    mutationFn: (body: OnboardingProgressInput) => {
      if (!writer) throw new StaleApiRequestContextError();
      return writer(body);
    },
    onSuccess: (state) => {
      if (!context) return;
      requireOnboardingAuthority(context);
      queryClient.setQueryData([...scope, "state"], state);
    },
  });
}
