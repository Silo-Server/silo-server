import { useQuery } from "@tanstack/react-query";

import { v2, type V2Result } from "@/api/v2/request";
import { adminKeys } from "@/hooks/queries/keys";

/** The policy engine capability document, as the v2 getPolicyCapability operation describes it. */
export type PolicyCapability = V2Result<"GET /api/v2/policy/capability">;

/**
 * The policy engine's capability for the signed-in account. `state` is
 * `available` once the engine is configured; an unconfigured engine answers
 * `not_configured` rather than an error, so consumers gate on the state.
 */
export function usePolicyCapability() {
  return useQuery({
    queryKey: adminKeys.policyCapability(),
    queryFn: () => v2("GET /api/v2/policy/capability"),
    staleTime: 30_000,
  });
}
