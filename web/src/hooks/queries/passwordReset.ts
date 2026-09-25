import { useQuery } from "@tanstack/react-query";
import { v2, V2TransportError } from "@/api/v2/request";

// The capability read lives here rather than in api/v2/publicPasswordResets:
// the sign-in page loads eagerly and must not pull in the reset screen's code.
async function getPasswordResetCapability(signal?: AbortSignal) {
  const result = await v2("GET /api/v2/capabilities/password-reset", {
    signal,
    retryAuthentication: false,
  });
  if (!result || typeof result.state !== "string") {
    throw new V2TransportError("getPasswordResetCapability", 200, "Invalid capability response");
  }
  return result;
}

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
