/**
 * People search, kept apart from the rest of the people hooks because the
 * global search dialog loads with every page.
 */
import { type QueryClient, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  getPeopleSearchCapabilities,
  searchPeople,
  type PersonSearchMediaScope,
} from "@/api/v2/people";

import { personKeys } from "./keys";

export function fetchPeopleSearchCapabilities(queryClient: QueryClient) {
  return queryClient.fetchQuery({
    queryKey: personKeys.searchCapabilities(),
    queryFn: ({ signal }) => getPeopleSearchCapabilities({ signal }),
    staleTime: 5 * 60 * 1000,
  });
}

/**
 * Whether the server takes the extended rule fields and "is not in the last"
 * (`extended_query_rules`). False until the capability answers, so a rule an
 * older server would refuse is not offered meanwhile.
 */
export function useExtendedQueryRules(): boolean {
  const { data } = useQuery({
    queryKey: personKeys.searchCapabilities(),
    queryFn: ({ signal }) => getPeopleSearchCapabilities({ signal }),
    staleTime: 5 * 60 * 1000,
  });
  return data?.extended_query_rules === true;
}

export function usePersonSearch(
  query: string,
  limit = 20,
  enabled = true,
  mediaScope?: PersonSearchMediaScope,
) {
  const normalizedQuery = query.trim();
  const queryClient = useQueryClient();

  return useQuery({
    queryKey: personKeys.search(normalizedQuery, limit, mediaScope),
    queryFn: async ({ signal }) => {
      const capabilities = await fetchPeopleSearchCapabilities(queryClient);
      // This capability also guarantees viewer access filtering for All.
      if (!capabilities.people_media_scope) return [];
      return searchPeople(normalizedQuery, limit, { signal, mediaScope });
    },
    enabled: enabled && normalizedQuery.length > 0,
    staleTime: 5 * 60 * 1000,
  });
}
