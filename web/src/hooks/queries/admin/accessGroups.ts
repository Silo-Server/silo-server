import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { captureProfileRequestContext } from "@/api/client";
import type { AccessGroupInput } from "@/api/types";
import {
  getAccessGroupCapabilities,
  accessGroupScope,
  captureAccessGroupAuthority,
  createAccessGroup,
  deleteAccessGroup,
  listAccessGroups,
  updateAccessGroup,
  type AccessGroupEditor,
} from "@/api/v2/accessGroups";
import { adminKeys } from "../keys";

export const accessGroupsKey = (scope = accessGroupScope()) => [...adminKeys.accessGroups(), scope];
export function useAccessGroups() {
  const context = captureProfileRequestContext();
  const scope = accessGroupScope(context);
  return useQuery({
    queryKey: accessGroupsKey(scope),
    queryFn: () => listAccessGroups(context ?? captureAccessGroupAuthority()),
    enabled: context !== null,
    retry: false,
    staleTime: 30_000,
  });
}
export function useCreateAccessGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      body,
      profileContext,
    }: {
      body: AccessGroupInput;
      profileContext: ReturnType<typeof captureAccessGroupAuthority>;
    }) => createAccessGroup(body, profileContext),
    retry: false,
    onSuccess: (_data, variables) => {
      void queryClient.invalidateQueries({
        queryKey: accessGroupsKey(accessGroupScope(variables.profileContext)),
      });
    },
  });
}
export function useUpdateAccessGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ editor, body }: { editor: AccessGroupEditor; body: AccessGroupInput }) =>
      updateAccessGroup(editor, body),
    retry: false,
    onSuccess: (_data, { editor }) => {
      void queryClient.invalidateQueries({
        queryKey: accessGroupsKey(accessGroupScope(editor.profileContext)),
      });
      void queryClient.invalidateQueries({
        queryKey: [...adminKeys.users(), accessGroupScope(editor.profileContext)],
      });
    },
  });
}
export function useDeleteAccessGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: deleteAccessGroup,
    retry: false,
    onSuccess: (_data, editor) => {
      void queryClient.invalidateQueries({
        queryKey: accessGroupsKey(accessGroupScope(editor.profileContext)),
      });
      void queryClient.invalidateQueries({
        queryKey: [...adminKeys.users(), accessGroupScope(editor.profileContext)],
      });
    },
  });
}

export function useAccessGroupCapabilities() {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...accessGroupsKey(accessGroupScope(context)), "capabilities"],
    queryFn: () => getAccessGroupCapabilities(context ?? captureAccessGroupAuthority()),
    enabled: context !== null,
    retry: false,
    staleTime: 30_000,
  });
}
