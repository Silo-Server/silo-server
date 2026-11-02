import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { v2 } from "@/api/v2/request";
import { adminAuthorityScope } from "@/api/v2/adminAuthority";
import {
  adminSettingsKey,
  captureSettingsBaseline,
  type ChatGPTSettingsChange,
  type SettingsValues,
} from "@/api/v2/adminSettingsSnapshot";
import { metadataAIStatusKey } from "@/hooks/queries/metadataAI";

const connectionKey = ["admin", "ai", "chatgpt"] as const;

function scopedConnectionKey(context: ProfileRequestContextSnapshot | null) {
  return [
    ...connectionKey,
    adminAuthorityScope(context),
    context?.profileTokenGeneration ?? 0,
  ] as const;
}

export function useAdminAICapabilities(model: string) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [
      "admin",
      "ai",
      "capabilities",
      model,
      adminAuthorityScope(context),
      context?.profileTokenGeneration ?? 0,
    ],
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
    queryKey: [...scopedConnectionKey(context), "status"],
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
    queryKey: [...scopedConnectionKey(context), "models", clientID],
    enabled: enabled && context !== null && clientID !== "",
    queryFn: () => {
      if (!context) throw new StaleApiRequestContextError();
      return v2("GET /api/v2/admin/ai/chatgpt/models", {
        query: { client_id: clientID },
        profileContext: context,
      });
    },
    retry: false,
    staleTime: 60_000,
  });
}

function useConnectionWrite<TInput, TResult>(
  request: (input: TInput, context: ProfileRequestContextSnapshot) => Promise<TResult>,
  options: {
    changesReadiness?: boolean;
    afterSettingsRefresh?: (result: TResult, settings: SettingsValues | undefined) => void;
  } = {},
) {
  const qc = useQueryClient();
  const context = captureProfileRequestContext();
  return useMutation({
    gcTime: 0,
    retry: false,
    mutationFn: async (input: TInput) => {
      if (!context || !isCapturedProfileAuthorityActive(context))
        throw new StaleApiRequestContextError();
      let result: TResult;
      try {
        result = await request(input, context);
      } catch (error) {
        if (isCapturedProfileAuthorityActive(context)) {
          await qc.invalidateQueries({ queryKey: scopedConnectionKey(context) }).catch(() => {
            // Preserve the request error if the connection refresh fails.
          });
        }
        throw error;
      } finally {
        if (options.changesReadiness && isCapturedProfileAuthorityActive(context)) {
          // A failed write can still deactivate the account on the server.
          await qc.invalidateQueries({ queryKey: metadataAIStatusKey, exact: true }).catch(() => {
            // Preserve the write outcome if the capability refresh fails.
          });
        }
      }
      if (!isCapturedProfileAuthorityActive(context)) throw new StaleApiRequestContextError();
      await qc.invalidateQueries({ queryKey: scopedConnectionKey(context) });
      if (options.afterSettingsRefresh && isCapturedProfileAuthorityActive(context)) {
        await qc.invalidateQueries({ queryKey: adminSettingsKey(context), exact: true });
        if (isCapturedProfileAuthorityActive(context)) {
          options.afterSettingsRefresh(result, qc.getQueryData(adminSettingsKey(context)));
        }
      }
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
  return useConnectionWrite(
    (callbackURL: string, profileContext) =>
      captureActivationChange((onResponse) =>
        v2("POST /api/v2/admin/ai/chatgpt/login/complete", {
          body: { callback_url: callbackURL },
          profileContext,
          onResponse,
        }),
      ),
    { changesReadiness: true, afterSettingsRefresh: retainActivationSnapshot },
  );
}

export function useSelectChatGPTAccount() {
  return useConnectionWrite(
    (clientID: string, profileContext) =>
      captureActivationChange((onResponse) =>
        v2("POST /api/v2/admin/ai/chatgpt/accounts/{client_id}/select", {
          path: { client_id: clientID },
          profileContext,
          onResponse,
        }),
      ),
    { changesReadiness: true, afterSettingsRefresh: retainActivationSnapshot },
  );
}

async function captureActivationChange(
  request: (onResponse: (response: Response) => void) => Promise<unknown>,
): Promise<ChatGPTSettingsChange> {
  const change: ChatGPTSettingsChange = { beforeETag: "", afterETag: "" };
  await request((response) => {
    change.beforeETag = response.headers.get("X-Silo-Settings-Before-ETag") ?? "";
    change.afterETag = response.headers.get("X-Silo-Settings-After-ETag") ?? "";
    change.firstConnection = response.headers.get("X-Silo-ChatGPT-First-Connection") === "true";
  });
  return change;
}

function retainActivationSnapshot(
  change: ChatGPTSettingsChange,
  settings: SettingsValues | undefined,
) {
  if (!change.beforeETag || !change.afterETag || !settings) return;
  try {
    if (captureSettingsBaseline(settings).etag === change.afterETag)
      change.settingsSnapshot = settings;
  } catch {
    // A missing or stale refresh cannot acknowledge a settings transition.
  }
}

export function useDisconnectChatGPTAccount() {
  return useConnectionWrite(
    (clientID: string, profileContext) =>
      v2("DELETE /api/v2/admin/ai/chatgpt/accounts/{client_id}", {
        path: { client_id: clientID },
        profileContext,
      }),
    { changesReadiness: true },
  );
}
