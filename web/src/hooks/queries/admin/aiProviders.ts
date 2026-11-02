import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { v2 } from "@/api/v2/request";
import type { components } from "@/api/v2/schema";
import { adminKeys } from "../keys";

export type AIProvider = components["schemas"]["AdminAIProvider"];
export type AIProviderType = components["schemas"]["AdminAIProviderType"];
export type AIModel = components["schemas"]["AdminAIModel"];
export type AIProviderCheck = components["schemas"]["AdminAIProviderCheck"];
export type AICapability = AIProvider["capabilities"][number];

const providersKey = ["admin", "ai", "providers"] as const;

export function useAIProviders() {
  return useQuery({
    queryKey: providersKey,
    queryFn: ({ signal }) => v2("GET /api/v2/admin/ai/providers", { signal }),
    staleTime: 30_000,
  });
}

/**
 * Refetches the provider list, whose `used_by` follows the job settings: call
 * it after a save that can change which provider a job uses.
 */
export function useRefreshAIProviders() {
  const qc = useQueryClient();
  return () => qc.invalidateQueries({ queryKey: providersKey });
}

/** The models a stored provider lists, fetched when a job selects it. */
export function useAIProviderModels(id: string) {
  return useQuery({
    queryKey: [...providersKey, id, "models"],
    queryFn: ({ signal }) =>
      v2("GET /api/v2/admin/ai/providers/{id}/models", { path: { id }, signal }),
    enabled: id !== "",
    staleTime: 5 * 60_000,
    retry: false,
  });
}

/**
 * Refreshes the list after a write, and the settings when the write can clear
 * a job's binding (a delete of an idle provider does).
 */
function useProviderWrite<TInput, TResult>(
  request: (input: TInput) => Promise<TResult>,
  { changesSettings = false } = {},
) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: request,
    retry: false,
    onSettled: async () => {
      await Promise.all([
        qc.invalidateQueries({ queryKey: providersKey }),
        changesSettings
          ? qc.invalidateQueries({ queryKey: adminKeys.serverSettings() })
          : Promise.resolve(),
      ]);
    },
  });
}

export interface AIProviderDraft {
  type: AIProviderType["type"];
  name: string;
  base_url: string;
  /** Undefined keeps the stored key; an empty string clears it. */
  api_key?: string;
  capabilities: AICapability[];
}

export function useCreateAIProvider() {
  return useProviderWrite((draft: AIProviderDraft) =>
    v2("POST /api/v2/admin/ai/providers", {
      body: {
        type: draft.type,
        name: draft.name,
        base_url: draft.base_url,
        api_key: draft.api_key || undefined,
        capabilities: draft.capabilities,
      },
    }),
  );
}

export function useUpdateAIProvider() {
  return useProviderWrite(({ id, draft }: { id: string; draft: AIProviderDraft }) =>
    v2("PATCH /api/v2/admin/ai/providers/{id}", {
      path: { id },
      body: {
        name: draft.name,
        base_url: draft.base_url,
        api_key: draft.api_key,
        capabilities: draft.type === "custom" ? draft.capabilities : undefined,
      },
    }),
  );
}

export function useDeleteAIProvider() {
  return useProviderWrite(
    (id: string) => v2("DELETE /api/v2/admin/ai/providers/{id}", { path: { id } }),
    { changesSettings: true },
  );
}

export function useCheckAIProvider() {
  return useMutation({
    mutationFn: ({ id, draft }: { id?: string; draft: AIProviderDraft }) =>
      v2("POST /api/v2/admin/ai/providers/check", {
        body: { id, type: draft.type, base_url: draft.base_url, api_key: draft.api_key },
      }),
    retry: false,
  });
}
