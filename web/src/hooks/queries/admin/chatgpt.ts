import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
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

function useConnectionWrite<TInput, TResult>(
  request: (input: TInput, context: ProfileRequestContextSnapshot) => Promise<TResult>,
) {
  const qc = useQueryClient();
  const context = captureProfileRequestContext();
  return useMutation({
    gcTime: 0,
    retry: false,
    mutationFn: async (input: TInput) => {
      if (!context || !isCapturedProfileAuthorityActive(context))
        throw new StaleApiRequestContextError();
      const result = await request(input, context);
      if (!isCapturedProfileAuthorityActive(context)) throw new StaleApiRequestContextError();
      await qc.invalidateQueries({
        queryKey: connectionKey,
        predicate: (query) => {
          const authority = query.queryKey.at(-1) as ProfileRequestContextSnapshot | null;
          return (
            authority?.authContextVersion === context.authContextVersion &&
            authority.serverOrigin === context.serverOrigin &&
            authority.profileId === context.profileId &&
            authority.profileTokenGeneration === context.profileTokenGeneration
          );
        },
      });
      if (!isCapturedProfileAuthorityActive(context)) throw new StaleApiRequestContextError();
      return result;
    },
  });
}

export function useStartChatGPTLogin() {
  return useConnectionWrite((clientID: string | undefined, profileContext) =>
    v2("POST /api/v2/admin/ai/chatgpt/login", {
      body: { client_id: clientID },
      profileContext,
    }),
  );
}

export function useCompleteChatGPTLogin() {
  return useConnectionWrite((callbackURL: string, profileContext) =>
    v2("POST /api/v2/admin/ai/chatgpt/login/complete", {
      body: { callback_url: callbackURL },
      profileContext,
    }),
  );
}

export function useSelectChatGPTAccount() {
  return useConnectionWrite((clientID: string, profileContext) =>
    v2("POST /api/v2/admin/ai/chatgpt/accounts/{client_id}/select", {
      path: { client_id: clientID },
      profileContext,
    }),
  );
}

export function useDisconnectChatGPTAccount() {
  return useConnectionWrite((clientID: string, profileContext) =>
    v2("DELETE /api/v2/admin/ai/chatgpt/accounts/{client_id}", {
      path: { client_id: clientID },
      profileContext,
    }),
  );
}
