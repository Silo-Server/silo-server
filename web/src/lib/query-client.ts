import { QueryClient } from "@tanstack/react-query";

/**
 * A 401 or 403 describes the caller, not a transient fault: the session layer
 * has already tried a token refresh, so sending the same request again cannot
 * change the answer. Every client error class (v2 problem, v2 transport, v1)
 * carries the HTTP status as `status`.
 */
function isAuthorizationRefusal(error: unknown): boolean {
  const status = error instanceof Error ? (error as { status?: unknown }).status : undefined;
  return status === 401 || status === 403;
}

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 2 * 60_000,
      gcTime: 10 * 60_000,
      retry: (failureCount, error) => failureCount < 1 && !isAuthorizationRefusal(error),
      refetchOnWindowFocus: false,
      refetchOnReconnect: true,
      throwOnError: false,
    },
    mutations: {
      retry: 0,
    },
  },
});
