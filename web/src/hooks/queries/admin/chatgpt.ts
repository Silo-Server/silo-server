import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { captureProfileRequestContext, StaleApiRequestContextError } from "@/api/client";
import { v2 } from "@/api/v2/request";

const connectionKey = ["admin", "ai", "chatgpt"] as const;

export function useAdminAICapabilities(model: string) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: ["admin", "ai", "capabilities", model, context],
    enabled: context !== null,
    queryFn: () => {
      if (!context) throw new StaleApiRequestContextError();
      return v2("GET /api/v2/admin/ai/capabilities", { query: { model }, profileContext: context });
    },
    staleTime: 5 * 60_000,
    retry: false,
  });
}

export function useChatGPTStatus(poll: boolean, enabled = true) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...connectionKey, "status", context],
    enabled: enabled && context !== null,
    queryFn: () => {
      if (!context) throw new StaleApiRequestContextError();
      return v2("GET /api/v2/admin/ai/chatgpt", { profileContext: context });
    },
    refetchInterval: poll ? 2_000 : false,
    retry: false,
    staleTime: 0,
  });
}

export function useChatGPTModels(clientID: string, enabled: boolean) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...connectionKey, "models", clientID, context],
    enabled: enabled && context !== null,
    queryFn: () => {
      if (!context) throw new StaleApiRequestContextError();
      return v2("GET /api/v2/admin/ai/chatgpt/models", { profileContext: context });
    },
    retry: false,
    staleTime: 60_000,
  });
}

export function useStartChatGPTLogin() {
  const qc = useQueryClient();
  return useMutation({
    gcTime: 0,
    retry: false,
    mutationFn: (clientID?: string) =>
      v2("POST /api/v2/admin/ai/chatgpt/login", {
        body: { client_id: clientID },
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: connectionKey }),
  });
}

export function useCompleteChatGPTLogin() {
  const qc = useQueryClient();
  return useMutation({
    gcTime: 0,
    retry: false,
    mutationFn: (callbackURL: string) =>
      v2("POST /api/v2/admin/ai/chatgpt/login/complete", {
        body: { callback_url: callbackURL },
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: connectionKey }),
  });
}

export function useSelectChatGPTAccount() {
  const qc = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (clientID: string) =>
      v2("POST /api/v2/admin/ai/chatgpt/accounts/{client_id}/select", {
        path: { client_id: clientID },
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: connectionKey }),
  });
}

export function useDisconnectChatGPTAccount() {
  const qc = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (clientID: string) =>
      v2("DELETE /api/v2/admin/ai/chatgpt/accounts/{client_id}", {
        path: { client_id: clientID },
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: connectionKey }),
  });
}
