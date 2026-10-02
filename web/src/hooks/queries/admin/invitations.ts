import {
  useQuery,
  useInfiniteQuery,
  useMutation,
  useQueryClient,
  type InfiniteData,
} from "@tanstack/react-query";
import {
  captureInvitationAuthority,
  invitationScope,
  getAdminInvitationCapabilities,
  listAdminInvitationsPage,
  createAdminInvitation,
  resendAdminInvitation,
  revokeAdminInvitation,
  type InvitationAuthority,
  type InvitationPage,
  type CreateInvitationBody,
} from "@/api/v2/invitations";
import { adminKeys } from "../keys";
const key = (scope: string) => [...adminKeys.invitations(), scope] as const;
const staleTime = 30_000;
export function useInvitationCapabilities() {
  const scope = invitationScope();
  return useQuery({
    queryKey: [...key(scope), "capabilities"],
    queryFn: () => {
      const c = captureInvitationAuthority();
      if (invitationScope(c) !== scope) throw new Error("Invitation authority changed.");
      return getAdminInvitationCapabilities(c);
    },
    retry: false,
    staleTime,
  });
}
export function useAdminInvitations(enabled = true) {
  const client = useQueryClient();
  const scope = invitationScope();
  const queryKey = key(scope);
  const query = useInfiniteQuery({
    queryKey,
    enabled,
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const c = captureInvitationAuthority();
      if (invitationScope(c) !== scope) throw new Error("Invitation authority changed.");
      const page = await listAdminInvitationsPage(pageParam, c);
      const prior = client.getQueryData<InfiniteData<InvitationPage, string | undefined>>(queryKey);
      const index = prior?.pageParams.indexOf(pageParam) ?? -1;
      const visited = index < 0 ? prior?.pageParams : prior?.pageParams.slice(0, index);
      if (page.page.has_more && visited?.includes(page.page.next_cursor))
        throw new Error("Repeated invitation cursor. Reload history.");
      return page;
    },
    getNextPageParam: (p) => (p.page.has_more ? p.page.next_cursor : undefined),
    retry: false,
    staleTime,
  });
  return {
    ...query,
    restart: () => client.resetQueries({ queryKey, exact: true }, { throwOnError: true }),
  };
}
export function useCreateInvitation() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({
      body,
      profileContext,
    }: {
      body: CreateInvitationBody;
      profileContext: InvitationAuthority;
    }) => createAdminInvitation(body, profileContext),
    retry: false,
    gcTime: 0,
    onSuccess: (_result, v) => {
      void client.invalidateQueries({
        queryKey: key(invitationScope(v.profileContext)),
        exact: true,
      });
    },
  });
}
export function useResendInvitation() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ id, profileContext }: { id: string; profileContext: InvitationAuthority }) =>
      resendAdminInvitation(id, profileContext),
    retry: false,
    gcTime: 0,
    onSuccess: (_result, v) => {
      void client.invalidateQueries({
        queryKey: key(invitationScope(v.profileContext)),
        exact: true,
      });
    },
  });
}
export function useRevokeInvitation() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ id, profileContext }: { id: string; profileContext: InvitationAuthority }) =>
      revokeAdminInvitation(id, profileContext),
    retry: false,
    gcTime: 0,
    onSuccess: (_result, v) => {
      void client.invalidateQueries({
        queryKey: key(invitationScope(v.profileContext)),
        exact: true,
      });
    },
  });
}
