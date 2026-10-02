import { QueryClient } from "@tanstack/react-query";

import { V2ProblemError } from "@/api/v2/request";

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

/**
 * A 404 problem is the server saying there is nothing at that address for this
 * caller, and asking again gives the same answer; retrying only holds a
 * not-found page behind a second request. A 404 without a problem document
 * (a proxy or gateway page) says nothing about the resource, so it still
 * retries.
 */
function isNotFoundAnswer(error: unknown): boolean {
  return error instanceof V2ProblemError && error.status === 404;
}

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 2 * 60_000,
      gcTime: 10 * 60_000,
      retry: (failureCount, error) =>
        failureCount < 1 && !isAuthorizationRefusal(error) && !isNotFoundAnswer(error),
      refetchOnWindowFocus: false,
      refetchOnReconnect: true,
      throwOnError: false,
    },
    mutations: {
      retry: 0,
    },
  },
});
