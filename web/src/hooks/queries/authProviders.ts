import { queryOptions, type QueryClient } from "@tanstack/react-query";

import { v2 } from "@/api/v2/request";

export const authProviderKeys = {
  all: () => ["auth", "providers"] as const,
};

/** Public discovery shared by the auth context and live provider changes. */
export function authProviderQueryOptions() {
  return queryOptions({
    queryKey: authProviderKeys.all(),
    queryFn: () => v2("GET /api/v2/auth/providers", { retryAuthentication: false }),
    // The login page can mount just after boot fetched the same discovery.
    staleTime: 1_000,
    retry: false,
  });
}

/** Provider writes must refresh even when discovery was read moments ago. */
export async function refreshAuthProviders(queryClient: QueryClient): Promise<void> {
  // A surface that has never read discovery has no provider view to refresh.
  if (!queryClient.getQueryState(authProviderKeys.all())) return;
  try {
    await queryClient.fetchQuery({ ...authProviderQueryOptions(), staleTime: 0 });
  } catch {
    // A discovery outage must not turn a saved provider change into a failure.
  }
}
