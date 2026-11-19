import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createPersonalApiKey,
  getPersonalApiKeyScopes,
  listPersonalApiKeysPage,
  revokePersonalApiKey,
} from "@/api/v2/personalApiKeys";

// The list differs per acting profile: a profile other than the primary one
// sees only its own keys.
const listKey = (profileId: string) => ["personalApiKeys", "list", profileId] as const;

export function usePersonalApiKeyScopes() {
  return useQuery({
    queryKey: ["personalApiKeys", "scopes"],
    queryFn: getPersonalApiKeyScopes,
    staleTime: 30_000,
  });
}

export function usePersonalApiKeys(profileId: string) {
  return useInfiniteQuery({
    queryKey: listKey(profileId),
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) => listPersonalApiKeysPage(pageParam),
    getNextPageParam: (last) => last.nextCursor,
    retry: false,
  });
}

export function useCreatePersonalApiKey(profileId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: createPersonalApiKey,
    retry: false,
    gcTime: 0,
    onSettled: () => void queryClient.invalidateQueries({ queryKey: listKey(profileId) }),
  });
}

export function useRevokePersonalApiKey(profileId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: revokePersonalApiKey,
    retry: false,
    onSettled: () => void queryClient.invalidateQueries({ queryKey: listKey(profileId) }),
  });
}
