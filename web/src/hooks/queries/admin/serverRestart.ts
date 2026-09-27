import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { v2 } from "@/api/v2/request";
import { adminKeys } from "../keys";

/** Optional notice shown to active playback sessions before the process exits. */
export interface ServerRestartRequest {
  reason?: string;
  title?: string;
  message?: string;
}
export interface ServerRestartResult {
  status: "restart_requested" | "already_requested";
  message: string;
  notified_sessions: number;
}
type ServerRestartIntent = {
  body: ServerRestartRequest;
  profileContext: ProfileRequestContextSnapshot | null;
};

/**
 * Asks the API process that answers to shut down gracefully so its supervisor
 * restarts it. The command is coalescing: a repeat while shutdown is pending
 * answers already_requested. The intent captures its authority at confirm time,
 * sends once with no authentication replay, and refuses to run after the
 * authority changed. Nothing here proves the new process is up; the admin shell
 * observes that through server status.
 */
export function useRequestServerRestart() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    retry: false,
    mutationFn: async (intent: ServerRestartIntent): Promise<ServerRestartResult> => {
      if (!intent.profileContext || !isCapturedProfileAuthorityActive(intent.profileContext))
        throw new StaleApiRequestContextError();
      const result = await v2("POST /api/v2/admin/server/restart", {
        body: intent.body,
        profileContext: intent.profileContext,
        retryAuthentication: false,
      });
      if (!isCapturedProfileAuthorityActive(intent.profileContext))
        throw new StaleApiRequestContextError();
      return result;
    },
    onSettled: (_result, _error, intent) => {
      if (intent.profileContext && isCapturedProfileAuthorityActive(intent.profileContext))
        void queryClient.invalidateQueries({ queryKey: adminKeys.serverStatus() });
    },
  });
  return {
    ...mutation,
    mutateAsync: (body: ServerRestartRequest = {}) =>
      mutation.mutateAsync({ body: { ...body }, profileContext: captureProfileRequestContext() }),
  };
}
