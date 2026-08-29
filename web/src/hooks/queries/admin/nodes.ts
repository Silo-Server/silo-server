import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type {
  StreamNode,
  CreateNodeRequest,
  UpdateNodeRequest,
  CheckNodeResponse,
  ReprobeNodeResult,
} from "@/api/types";
import { adminKeys } from "../keys";
import { usePageActivity } from "@/hooks/usePageActivity";
import { describeReprobeOutcome } from "@/pages/adminNodesPresentation";
import { toast } from "sonner";

const ADMIN_STALE_TIME = 30_000;

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

  return useQuery({
    queryKey: adminKeys.nodes(),
    queryFn: () => api<StreamNode[]>("/admin/nodes").then((d) => d ?? []),
    staleTime: ADMIN_STALE_TIME,
    refetchInterval: pageActivity.canApplyRealtimeUpdates ? ADMIN_STALE_TIME : false,
  });
}

export function useCreateNode() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateNodeRequest) =>
      api("/admin/nodes", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      toast.success("Node created");
      queryClient.invalidateQueries({ queryKey: adminKeys.nodes() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save");
    },
  });
}

export function useUpdateNode() {
  const queryClient = useQueryClient();
  return useMutation({
    // The body is typed rather than a loose record so a null acceleration
    // override — the value that restores inheritance of the cluster-wide
    // setting — survives to the wire instead of being dropped as a typo.
    mutationFn: ({ id, body }: { id: number; body: UpdateNodeRequest }) =>
      api<StreamNode>(`/admin/nodes/${id}`, {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: adminKeys.nodes() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to update node");
    },
  });
}

export function useDeleteNode() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api(`/admin/nodes/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Node deleted");
      queryClient.invalidateQueries({ queryKey: adminKeys.nodes() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to delete node");
    },
  });
}

export function useCheckNodeHealth() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (node: StreamNode) =>
      api<CheckNodeResponse>(`/admin/nodes/${node.id}/check`, {
        method: "POST",
      }).then((result) => ({ node, result })),
    onSuccess: ({ node, result }) => {
      toast.success(result.healthy ? `${node.name} is healthy` : `${node.name} is unhealthy`);
      queryClient.invalidateQueries({ queryKey: adminKeys.nodes() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Health check failed");
    },
  });
}

/**
 * Ask one node to re-verify its hardware against live devices.
 *
 * The call always answers 200 — a node that refused or could not be reached is
 * reported in the body — so the outcome is read from `status`, not from a
 * thrown error. It can take a couple of minutes on a node with several devices,
 * since the point is to pay the full cold probe cost the node otherwise caches
 * away for its process lifetime; the server extends the connection's write
 * deadline to cover that, so a long wait here is the action working, not a hung
 * request. A node that is transcoding refuses, because the probe encodes on the
 * GPU and a busy encoder would report working hardware as failed.
 *
 * The nodes list is invalidated either way: on success the server has already
 * stored the fresh report, and on failure the row may still have moved.
 */
export function useReprobeNode() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (node: StreamNode) =>
      api<ReprobeNodeResult>(`/admin/nodes/${node.id}/reprobe`, {
        method: "POST",
      }).then((result) => ({ node, result })),
    onSuccess: ({ node, result }) => {
      const outcome = describeReprobeOutcome(node, result);
      if (outcome.ok) {
        toast.success(outcome.message);
      } else {
        toast.error(outcome.message);
      }
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Re-probe failed");
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: adminKeys.nodes() });
    },
  });
}

export function useToggleNode() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (node: StreamNode) =>
      api<StreamNode>(`/admin/nodes/${node.id}`, {
        method: "PUT",
        body: JSON.stringify({ enabled: !node.enabled }),
      }),
    onSuccess: (updated) => {
      toast.success(`${updated.name} ${updated.enabled ? "enabled" : "disabled"}`);
      queryClient.invalidateQueries({ queryKey: adminKeys.nodes() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to update node");
    },
  });
}
