import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { v2 } from "@/api/v2/request";
import type { components } from "@/api/v2/schema";
import { usePageActivity } from "@/hooks/usePageActivity";

import { adminKeys } from "../keys";

export type NetworkAccessCapabilities = components["schemas"]["NetworkAccessCapabilities"];
export type NetworkAccessProviderSummary = components["schemas"]["NetworkAccessProviderSummary"];
export type NetworkAccessStatus = components["schemas"]["NetworkAccessStatus"];
export type NetworkAccessHostStatus = components["schemas"]["NetworkAccessHostStatus"];
export type NetworkAccessHostState = NetworkAccessHostStatus["state"];

/**
 * Provider status is asked of the plugin on every read, so polling costs a
 * plugin RPC per host each time; 15 s follows enrollment closely enough
 * (the auth URL appears, then connected) without hammering the plugin.
 */
const STATUS_POLL_INTERVAL = 15_000;

/** The installed overlay-network providers, read from plugin manifests. */
export function useNetworkAccessCapabilities() {
  const profileContext = captureProfileRequestContext();
  return useQuery({
    queryKey: [...adminKeys.networkAccessCapabilities(), profileContext?.authContextVersion],
    enabled: profileContext !== null,
    queryFn: async (): Promise<NetworkAccessCapabilities> => {
      if (!profileContext || !isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const result = await v2("GET /api/v2/network-access/capabilities", { profileContext });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      return result;
    },
    staleTime: 30_000,
  });
}

/** One provider's state on every host, polled while the page is in front. */
export function useAdminNetworkAccessStatus(provider: string | null) {
  const pageActivity = usePageActivity();
  const profileContext = captureProfileRequestContext();
  return useQuery({
    queryKey: [
      ...adminKeys.networkAccessStatus(provider ?? ""),
      profileContext?.authContextVersion,
      profileContext?.profileId,
    ],
    enabled: profileContext !== null && provider !== null,
    queryFn: async (): Promise<NetworkAccessStatus> => {
      if (!profileContext || !provider || !isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const result = await v2("GET /api/v2/admin/network-access/{provider}/status", {
        path: { provider },
        profileContext,
      });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      return result;
    },
    staleTime: STATUS_POLL_INTERVAL,
    refetchInterval: pageActivity.canApplyRealtimeUpdates ? STATUS_POLL_INTERVAL : false,
  });
}

export interface NetworkAccessCommandRequest {
  provider: string;
  /** Host ids to act on; omitted means every host. */
  hosts?: string[];
}
type NetworkAccessCommandIntent = NetworkAccessCommandRequest & {
  profileContext: ProfileRequestContextSnapshot | null;
};

/**
 * Connect and disconnect are acknowledgements: the server answers 202 with
 * the state each host reached within its timeout and enrollment may go on in
 * the background, so the result is written straight into the status query and
 * polling carries it from there. The intent captures its authority at click
 * time, sends once without authentication replay, and refuses to run after
 * the authority changed.
 */
function useNetworkAccessCommand(kind: "connect" | "disconnect") {
  const queryClient = useQueryClient();
  const failed = kind === "connect" ? "Failed to connect" : "Failed to disconnect";
  const mutation = useMutation({
    retry: false,
    mutationFn: async (intent: NetworkAccessCommandIntent): Promise<NetworkAccessStatus> => {
      if (!intent.profileContext || !isCapturedProfileAuthorityActive(intent.profileContext))
        throw new StaleApiRequestContextError();
      const options = {
        path: { provider: intent.provider },
        body: intent.hosts ? { hosts: intent.hosts } : {},
        profileContext: intent.profileContext,
        retryAuthentication: false,
      };
      const result =
        kind === "connect"
          ? await v2("POST /api/v2/admin/network-access/{provider}/connect", options)
          : await v2("POST /api/v2/admin/network-access/{provider}/disconnect", options);
      if (!isCapturedProfileAuthorityActive(intent.profileContext))
        throw new StaleApiRequestContextError();
      return result;
    },
    onSuccess: (result, intent) => {
      if (!intent.profileContext || !isCapturedProfileAuthorityActive(intent.profileContext))
        return;
      queryClient.setQueriesData(
        { queryKey: adminKeys.networkAccessStatus(intent.provider) },
        () => result,
      );
      void queryClient.invalidateQueries({
        queryKey: adminKeys.networkAccessStatus(intent.provider),
      });
    },
    onError: (err, intent) => {
      if (!intent.profileContext || !isCapturedProfileAuthorityActive(intent.profileContext))
        return;
      toast.error(err instanceof Error ? err.message : failed);
    },
  });
  return {
    ...mutation,
    mutate: (request: NetworkAccessCommandRequest) =>
      mutation.mutate({ ...request, profileContext: captureProfileRequestContext() }),
    mutateAsync: (request: NetworkAccessCommandRequest) =>
      mutation.mutateAsync({ ...request, profileContext: captureProfileRequestContext() }),
  };
}

export function useConnectNetworkAccess() {
  return useNetworkAccessCommand("connect");
}

export function useDisconnectNetworkAccess() {
  return useNetworkAccessCommand("disconnect");
}
