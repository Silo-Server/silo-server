import { useEffect, useRef, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  captureProfileRequestContext,
  captureSessionIdentity,
  getAccessToken,
  getProfileToken,
  getProfileTokenGeneration,
  isProfileRequestContextCurrent,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { v2 } from "@/api/v2/request";
import type {
  StreamNode,
  NodeCapabilities,
  NodeLastStats,
  CreateNodeRequest,
  UpdateNodeRequest,
  ReprobeNodeResult,
} from "@/api/types";
import { adminKeys } from "../keys";
import { usePageActivity } from "@/hooks/usePageActivity";
import { describeReprobeOutcome } from "@/pages/adminNodesPresentation";
import { toast } from "sonner";

const ADMIN_STALE_TIME = 30_000;

export async function fetchAdminNodes(
  profileContext = captureProfileRequestContext(),
): Promise<StreamNode[]> {
  if (!profileContext) throw new StaleApiRequestContextError();
  const nodes: StreamNode[] = [];
  let cursor: string | undefined;
  do {
    const page = await v2("GET /api/v2/admin/nodes", {
      profileContext,
      query: { limit: 200, cursor },
    });
    if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
    nodes.push(
      ...page.items.map((row) => ({
        ...row,
        capabilities: row.capabilities as NodeCapabilities | undefined,
        last_stats: row.last_stats as NodeLastStats | undefined,
      })),
    );
    cursor = page.page?.next_cursor;
  } while (cursor);
  return nodes;
}

/**
 * Polled on the node health cadence, because this row now carries live
 * readings rather than configuration.
 *
 * `staleTime` alone marks data old; it does not schedule anything. Without an
 * interval the GPU, disk and health columns froze at whatever they were when
 * the page mounted, refreshing only on focus, reconnect or a mutation — so an
 * operator watching a node saturate, a scratch volume fill, or a health check
 * start failing would see none of it. The server persists a fresh sample every
 * 30 seconds, so asking more often only costs requests.
 *
 * Gated on page activity: a backgrounded or frozen tab has nobody reading it,
 * and polling every admin tab a browser has open is how a small deployment
 * ends up serving its own dashboard.
 */
export function useAdminNodes() {
  const pageActivity = usePageActivity();
  const profileContext = captureProfileRequestContext();
  return useQuery({
    queryKey: [
      ...adminKeys.nodes(),
      profileContext?.serverOrigin,
      profileContext?.authContextVersion,
      profileContext?.profileId,
      profileContext?.profileTokenGeneration,
    ],
    enabled: profileContext !== null,
    queryFn: () => fetchAdminNodes(profileContext),
    staleTime: ADMIN_STALE_TIME,
    refetchInterval: pageActivity.canApplyRealtimeUpdates ? ADMIN_STALE_TIME : false,
  });
}

// Setup may have an administrator account before selecting a profile. An
// empty captured profile header represents that absence; no profile is chosen
// on the caller's behalf. The shared request still fences account/server changes.
function captureNodeWriteAuthority(): ProfileRequestContextSnapshot | null {
  const profile = captureProfileRequestContext();
  if (profile) return profile;
  const accessToken = getAccessToken();
  if (!accessToken) return null;
  return {
    ...captureSessionIdentity(),
    accessToken,
    profileId: "",
    profileToken: getProfileToken(),
    profileTokenGeneration: getProfileTokenGeneration(),
  };
}
export function isNodeWriteAuthorityActive(authority: ProfileRequestContextSnapshot): boolean {
  if (
    !isProfileRequestContextCurrent(authority) ||
    authority.profileTokenGeneration !== getProfileTokenGeneration()
  )
    return false;
  if (authority.profileId !== "") return isCapturedProfileAuthorityActive(authority);
  return (
    getAccessToken() !== null &&
    captureProfileRequestContext() === null &&
    getProfileToken() === authority.profileToken
  );
}

type NodeWriteIntent = {
  action: "create" | "update" | "delete";
  authority: ProfileRequestContextSnapshot | null;
  node?: StreamNode;
  body?: CreateNodeRequest | UpdateNodeRequest;
  onSuccess?: () => void;
};
function useNodeWrite() {
  const client = useQueryClient();
  const mounted = useRef(false);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  const mutation = useMutation({
    retry: false,
    mutationFn: async (intent: NodeWriteIntent) => {
      if (!mounted.current || !intent.authority || !isNodeWriteAuthorityActive(intent.authority))
        throw new StaleApiRequestContextError();
      const common = { profileContext: intent.authority, retryAuthentication: false };
      let result;
      let etag: string | null = null;
      const onResponse = (response: Response) => {
        etag = response.headers.get("ETag");
      };
      if (intent.action === "create") {
        result = await v2("POST /api/v2/admin/nodes", {
          ...common,
          body: intent.body as CreateNodeRequest,
          onResponse,
        });
      } else {
        if (!intent.node?.config_etag)
          throw new Error("Reload nodes and reopen this action to obtain its original version.");
        const options = {
          ...common,
          path: { id: String(intent.node.id) },
          headers: { "If-Match": intent.node.config_etag },
        };
        if (intent.action === "delete") {
          await v2("DELETE /api/v2/admin/nodes/{id}", options);
        } else {
          result = await v2("PUT /api/v2/admin/nodes/{id}", {
            ...options,
            body: intent.body as UpdateNodeRequest,
            onResponse,
          });
          if (result.id !== String(intent.node.id))
            throw new Error("Unexpected node write acknowledgement.");
        }
      }
      if (!mounted.current || !isNodeWriteAuthorityActive(intent.authority))
        throw new StaleApiRequestContextError();
      if (result && (!etag || result.config_etag !== etag))
        throw new Error(
          "Node write acknowledgement is unavailable. Inspect current state before saving again.",
        );
      return result;
    },
    onSuccess: (_result, intent) => {
      if (!mounted.current || !intent.authority || !isNodeWriteAuthorityActive(intent.authority))
        return;
      toast.success(intent.action === "delete" ? "Node deleted" : "Node configuration saved");
      intent.onSuccess?.();
    },
    onError: (_error, intent) => {
      if (mounted.current && intent.authority && isNodeWriteAuthorityActive(intent.authority))
        toast.error(
          "Node change could not be confirmed. Reload nodes and reopen the action before another submission; retained edits have not been rebased.",
        );
    },
    onSettled: (_result, _error, intent) => {
      if (intent.authority && isNodeWriteAuthorityActive(intent.authority))
        client.invalidateQueries({ queryKey: adminKeys.nodes() });
    },
  });
  return { ...mutation, isMounted: () => mounted.current };
}
export function useCreateNode() {
  const [authority] = useState(captureNodeWriteAuthority);
  const mutation = useNodeWrite();
  const intent = (
    body: CreateNodeRequest,
    options?: { onSuccess?: () => void },
  ): NodeWriteIntent => ({
    action: "create",
    authority,
    body: structuredClone(body),
    onSuccess: options?.onSuccess,
  });
  return {
    ...mutation,
    isAuthorityActive: () =>
      mutation.isMounted() && authority !== null && isNodeWriteAuthorityActive(authority),
    mutate: (body: CreateNodeRequest, options?: { onSuccess?: () => void }) =>
      mutation.mutate(intent(body, options)),
    mutateAsync: (body: CreateNodeRequest) => mutation.mutateAsync(intent(body)),
  };
}
export function useUpdateNode() {
  const [authority] = useState(captureNodeWriteAuthority);
  const mutation = useNodeWrite();
  return {
    ...mutation,
    mutate: (
      { node, body }: { node: StreamNode; body: UpdateNodeRequest },
      options?: { onSuccess?: () => void },
    ) =>
      mutation.mutate({
        action: "update",
        authority,
        node: structuredClone(node),
        body: structuredClone(body),
        onSuccess: options?.onSuccess,
      }),
  };
}
export type NodeDeleteIntent = {
  node: StreamNode;
  authority: ProfileRequestContextSnapshot | null;
};
export function useDeleteNode() {
  const mutation = useNodeWrite();
  return {
    ...mutation,
    mutate: (intent: NodeDeleteIntent) =>
      mutation.mutate({ action: "delete", ...structuredClone(intent) }),
  };
}

type NodeCommandIntent = { node: StreamNode; authority: ProfileRequestContextSnapshot };
function useNodeObservationCommand(action: "check" | "reprobe") {
  const queryClient = useQueryClient();
  const renderedAuthority = captureProfileRequestContext();
  const mutation = useMutation({
    retry: false,
    mutationFn: async (intent: NodeCommandIntent) => {
      if (!isCapturedProfileAuthorityActive(intent.authority))
        throw new StaleApiRequestContextError();
      const options = {
        path: { id: String(intent.node.id) },
        profileContext: intent.authority,
        retryAuthentication: false,
      };
      const result =
        action === "check"
          ? await v2("POST /api/v2/admin/nodes/{id}/check", options)
          : await v2("POST /api/v2/admin/nodes/{id}/reprobe", options);
      if (!isCapturedProfileAuthorityActive(intent.authority))
        throw new StaleApiRequestContextError();
      if ("node_id" in result && result.node_id !== String(intent.node.id))
        throw new Error("Unexpected node observation");
      return result;
    },
    onSuccess: (result, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.authority)) return;
      if ("healthy" in result) {
        toast.success(
          result.healthy ? `${intent.node.name} is healthy` : `${intent.node.name} is unhealthy`,
        );
        if (!result.health_persisted)
          toast.error("Health was observed but could not be stored. Refresh node state.");
      } else {
        const outcome = describeReprobeOutcome(intent.node, {
          ...result,
          node_id: Number(result.node_id),
        } as ReprobeNodeResult);
        if (outcome.ok) toast.success(outcome.message);
        else toast.error(outcome.message);
      }
    },
    onError: (_error, intent) => {
      if (isCapturedProfileAuthorityActive(intent.authority))
        toast.error(
          "Node command could not be confirmed. Inspect node state before another explicit command.",
        );
    },
    onSettled: (_result, _error, intent) => {
      if (isCapturedProfileAuthorityActive(intent.authority))
        queryClient.invalidateQueries({ queryKey: adminKeys.nodes() });
    },
  });
  return {
    ...mutation,
    variables: mutation.variables?.node,
    mutate: (node: StreamNode) => {
      if (!renderedAuthority || !isCapturedProfileAuthorityActive(renderedAuthority)) return;
      mutation.mutate({ node: structuredClone(node), authority: renderedAuthority });
    },
  };
}
export function useCheckNodeHealth() {
  return useNodeObservationCommand("check");
}
export function useReprobeNode() {
  return useNodeObservationCommand("reprobe");
}

export function useToggleNode() {
  const mutation = useNodeWrite();
  const authority = captureProfileRequestContext();
  return {
    ...mutation,
    variables: mutation.variables?.node,
    mutate: (node: StreamNode) =>
      mutation.mutate({
        action: "update",
        authority,
        node: structuredClone(node),
        body: { enabled: !node.enabled },
      }),
  };
}
