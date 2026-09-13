import {
  updateAdminSubtitleMetadata,
  type AdminSubtitleEditor,
  type AdminSubtitlePatch,
} from "@/api/v2/adminSubtitleMetadata";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  listSubtitleProviders,
  saveProviderConfiguration,
  type ProviderEditor,
  type ProviderChange,
} from "@/api/v2/adminSubtitleProviderConfiguration";
import {
  adminSubtitleListScope,
  listAdminSubtitles,
  type AdminSubtitleListQuery,
} from "@/api/v2/adminSubtitles";
import { v2 } from "@/api/v2/request";
import type { SubtitleProviderTestRequest } from "@/api/types";
import { adminKeys } from "../keys";
import { toast } from "sonner";

const ADMIN_STALE_TIME = 30_000;

export function useAdminDownloadedSubtitles(filters: AdminSubtitleListQuery) {
  const scope = adminSubtitleListScope();
  return useQuery({
    queryKey: ["admin", "downloadedSubtitles", scope, filters],
    queryFn: ({ signal }) => listAdminSubtitles(filters, scope, signal),
    retry: false,
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useAdminUpdateDownloadedSubtitle() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ editor, patch }: { editor: AdminSubtitleEditor; patch: AdminSubtitlePatch }) =>
      updateAdminSubtitleMetadata(editor, patch),
    retry: false,
    gcTime: 0,
    onSuccess: (_updated, { editor }) => {
      if (adminSubtitleListScope() !== editor.intent.scope) return;
      toast.success("Subtitle updated");
      void queryClient.invalidateQueries({
        queryKey: ["admin", "downloadedSubtitles", editor.intent.scope],
      });
    },
  });
}

export function useSubtitleProviders(enabled = true) {
  const scope = adminSubtitleListScope();
  const query = useQuery({
    queryKey: [...adminKeys.subtitleProviders(), scope],
    queryFn: ({ signal }) => listSubtitleProviders(scope, signal),
    enabled,
    retry: false,
    staleTime: ADMIN_STALE_TIME,
  });
  return { ...query, scope };
}

export function useUpdateSubtitleProvider() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ editor, config }: { editor: ProviderEditor; config: ProviderChange }) =>
      saveProviderConfiguration(editor, config),
    retry: false,
    gcTime: 0,
    onSuccess: (_saved, { editor }) => {
      if (adminSubtitleListScope() !== editor.intent.scope) return;
      void queryClient.invalidateQueries({
        queryKey: [...adminKeys.subtitleProviders(), editor.intent.scope],
      });
    },
  });
}

export function useTestSubtitleProvider() {
  return useMutation({
    mutationFn: ({ provider, config }: { provider: string; config: SubtitleProviderTestRequest }) =>
      testSubtitleProvider(provider, config),
    retry: false,
  });
}

export function testSubtitleProvider(provider: string, config: SubtitleProviderTestRequest) {
  return v2("POST /api/v2/admin/subtitle-providers/{provider}/test", {
    path: { provider },
    body: config,
    retryAuthentication: false,
  });
}
