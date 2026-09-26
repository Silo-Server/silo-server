import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  listWebhookConnections,
  createWebhookConnection,
  updateWebhookConnection,
  deleteWebhookConnection,
  rotateWebhookConnection,
  getWebhookMappings,
  updateWebhookMappings,
  listWebhookEvents,
} from "@/api/v2/webhookSync";
import type {
  CreateWebhookSyncConnectionRequest,
  UpdateWebhookSyncConnectionRequest,
  UpdateWebhookSyncProfileMappingsRequest,
} from "@/api/types";
import { webhookSyncKeys } from "./keys";
import { toast } from "sonner";

export function useWebhookSyncConnections() {
  return useQuery({
    queryKey: webhookSyncKeys.connections(),
    queryFn: () => listWebhookConnections(),
    staleTime: 15_000,
  });
}

export function useCreateWebhookSyncConnection() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (body: CreateWebhookSyncConnectionRequest) => createWebhookConnection(body),
    onSuccess: (result) => {
      toast.success("Webhook connection created");
      queryClient.invalidateQueries({ queryKey: webhookSyncKeys.connections() });
      queryClient.invalidateQueries({
        queryKey: webhookSyncKeys.profileMappings(result.connection.id),
      });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to create webhook connection");
    },
  });
}

export function useUpdateWebhookSyncConnection() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({
      connectionId,
      body,
    }: {
      connectionId: string;
      body: UpdateWebhookSyncConnectionRequest;
    }) => updateWebhookConnection(connectionId, body),
    onSuccess: (_, variables) => {
      toast.success("Webhook connection updated");
      queryClient.invalidateQueries({ queryKey: webhookSyncKeys.connections() });
      queryClient.invalidateQueries({
        queryKey: webhookSyncKeys.connection(variables.connectionId),
      });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to update webhook connection");
    },
  });
}

export function useDeleteWebhookSyncConnection() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (connectionId: string) => deleteWebhookConnection(connectionId),
    onSuccess: () => {
      toast.success("Webhook connection deleted");
      queryClient.invalidateQueries({ queryKey: webhookSyncKeys.connections() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to delete webhook connection");
    },
  });
}

export function useRotateWebhookSyncWebhook() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (connectionId: string) => rotateWebhookConnection(connectionId),
    onSuccess: (_, connectionId) => {
      toast.success("Webhook URL rotated");
      queryClient.invalidateQueries({ queryKey: webhookSyncKeys.connections() });
      queryClient.invalidateQueries({ queryKey: webhookSyncKeys.connection(connectionId) });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to rotate webhook URL");
    },
  });
}

export function useWebhookSyncProfileMappings(connectionId?: string) {
  return useQuery({
    queryKey: webhookSyncKeys.profileMappings(connectionId),
    queryFn: () => getWebhookMappings(connectionId!),
    enabled: !!connectionId,
    staleTime: 10_000,
  });
}

export function useWebhookSyncEvents(connectionId?: string) {
  return useQuery({
    queryKey: webhookSyncKeys.events(connectionId),
    queryFn: () => listWebhookEvents(connectionId!),
    enabled: !!connectionId,
    staleTime: 5_000,
    refetchInterval: connectionId ? 15_000 : false,
  });
}

export function useUpdateWebhookSyncProfileMappings() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({
      connectionId,
      body,
    }: {
      connectionId: string;
      body: UpdateWebhookSyncProfileMappingsRequest;
    }) => updateWebhookMappings(connectionId, body),
    onSuccess: (_, variables) => {
      toast.success("Profile mappings saved");
      queryClient.invalidateQueries({
        queryKey: webhookSyncKeys.profileMappings(variables.connectionId),
      });
      queryClient.invalidateQueries({ queryKey: webhookSyncKeys.connections() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save profile mappings");
    },
  });
}
