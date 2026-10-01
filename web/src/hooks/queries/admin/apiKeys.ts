import {
  useQuery,
  useInfiniteQuery,
  useMutation,
  useQueryClient,
  type InfiniteData,
} from "@tanstack/react-query";
import type { ProfileRequestContextSnapshot } from "@/api/client";
import {
  adminApiKeyScope,
  captureAdminApiKeyAuthority,
  createAdminApiKey,
  deleteAdminApiKey,
  getAdminApiKeyCapabilities,
  listAdminApiKeysPage,
  updateAdminApiKeyTier,
  type AdminAPIKeyEditor,
  type AdminAPIKeyMetadata,
  type AdminAPIKeyPage,
  type CreateBody,
} from "@/api/v2/adminApiKeys";
import { adminKeys } from "../keys";

const listKey = (scope: string) => [...adminKeys.apiKeys(), scope] as const;
const staleTime = 30_000;

export function useAdminApiKeyCapabilities() {
  const scope = adminApiKeyScope();
  return useQuery({
    queryKey: [...listKey(scope), "capabilities"],
    queryFn: () => {
      const context = captureAdminApiKeyAuthority();
      if (adminApiKeyScope(context) !== scope)
        throw new Error("Reload API keys after changing profiles.");
      return getAdminApiKeyCapabilities(context);
    },
    retry: false,
    staleTime,
  });
}
export function useAdminApiKeys(enabled = true) {
  const queryClient = useQueryClient();
  const scope = adminApiKeyScope();
  const queryKey = listKey(scope);
  const query = useInfiniteQuery({
    queryKey,
    enabled,
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const context = captureAdminApiKeyAuthority();
      if (adminApiKeyScope(context) !== scope)
        throw new Error("Reload API keys after changing profiles.");
      const page = await listAdminApiKeysPage(pageParam, context);
      const previous =
        queryClient.getQueryData<InfiniteData<AdminAPIKeyPage, string | undefined>>(queryKey);
      const currentIndex = previous?.pageParams.indexOf(pageParam) ?? -1;
      const visited =
        currentIndex >= 0 ? previous?.pageParams.slice(0, currentIndex) : previous?.pageParams;
      if (page.page.has_more && visited?.includes(page.page.next_cursor)) {
        throw new Error("Repeated API key cursor. Reload the list.");
      }
      return page;
    },
    getNextPageParam: (last) => (last.page.has_more ? last.page.next_cursor : undefined),
    retry: false,
    staleTime,
  });
  return { ...query, restart: () => queryClient.resetQueries({ queryKey, exact: true }) };
}
export function useAdminCreateApiKey() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      body,
      profileContext = captureAdminApiKeyAuthority(),
    }: {
      body: CreateBody;
      profileContext?: ProfileRequestContextSnapshot;
    }) => createAdminApiKey(body, profileContext),
    onMutate: (variables) => {
      variables.profileContext ??= captureAdminApiKeyAuthority();
    },
    retry: false,
    gcTime: 0,
    onSuccess: (_created, variables) => {
      void queryClient.invalidateQueries({
        queryKey: listKey(adminApiKeyScope(variables.profileContext)),
        exact: true,
      });
    },
  });
}
export function useAdminDeleteApiKey() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (editor: AdminAPIKeyEditor) => deleteAdminApiKey(editor),
    retry: false,
    gcTime: 0,
    onSuccess: (_result, editor) => {
      void queryClient.invalidateQueries({
        queryKey: listKey(adminApiKeyScope(editor.profileContext)),
        exact: true,
      });
    },
  });
}
export function useAdminUpdateApiKeyTier() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      editor,
      tier,
    }: {
      editor: AdminAPIKeyEditor;
      tier: AdminAPIKeyMetadata["rate_tier"];
    }) => updateAdminApiKeyTier(editor, tier),
    retry: false,
    gcTime: 0,
    onSuccess: (_result, { editor }) => {
      void queryClient.invalidateQueries({
        queryKey: listKey(adminApiKeyScope(editor.profileContext)),
        exact: true,
      });
    },
  });
}
