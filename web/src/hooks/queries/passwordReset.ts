import { useQuery } from "@tanstack/react-query";
import { getPasswordResetCapability } from "@/api/v2/publicPasswordResets";

export const PASSWORD_RESET_CAPABILITY_KEY = ["auth", "password-reset-capability"] as const;

/**
 * Whether this server offers self-service password reset. `available` is true
 * only from a fresh answer: a cached one from before an administrator turned
 * the feature off must not offer a request the server would refuse. A screen
 * that needs the answer only in some states passes `enabled` to skip the read
 * otherwise.
 */
export function usePasswordResetAvailable(enabled = true) {
  const query = useQuery({
    queryKey: PASSWORD_RESET_CAPABILITY_KEY,
    queryFn: ({ signal }) => getPasswordResetCapability(signal),
    refetchOnMount: "always",
    enabled,
  });
  return {
    available: query.isSuccess && query.isFetchedAfterMount && query.data.state === "available",
    // Settling: the first answer since mount, or a retry after a failure. A
    // background refetch of a settled answer is not pending.
    pending: !query.isFetchedAfterMount || (query.isError && query.isFetching),
    failed: query.isError && !query.isFetching,
    retry: () => void query.refetch(),
  };
}
