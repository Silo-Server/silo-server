import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { api } from "@/api/client";
import type { PushDeviceInfo, VapidKeyPair } from "@/api/types";
import { pushKeys } from "@/hooks/queries/keys";

export function useWebPushPublicKey() {
  return useQuery({
    queryKey: pushKeys.webpushKey(),
    queryFn: () => api<{ vapid_public_key: string }>("/notifications/push/webpush-key"),
    select: (d) => d?.vapid_public_key,
    staleTime: 5 * 60 * 1000,
  });
}

export function usePushDevices() {
  return useQuery({
    queryKey: pushKeys.devices(),
    queryFn: () => api<{ devices: PushDeviceInfo[] | null }>("/notifications/push/devices"),
    select: (d) => d?.devices ?? [],
  });
}

export function useTogglePushDevice() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ deviceId, enabled }: { deviceId: string; enabled: boolean }) =>
      api(`/notifications/push/devices/${deviceId}`, {
        method: "PUT",
        body: JSON.stringify({ enabled }),
      }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: pushKeys.devices() }),
    onError: (err) => toast.error(err instanceof Error ? err.message : "Failed to update device"),
  });
}

export function usePushStatus() {
  return useQuery({
    queryKey: pushKeys.status(),
    queryFn: () => api<{ apns: boolean; fcm: boolean; webpush: boolean }>("/admin/push/status"),
  });
}

export function useGenerateVapidKeys() {
  return useMutation({
    mutationFn: () => api<VapidKeyPair>("/admin/push/generate-vapid-keys", { method: "POST" }),
    onError: (err) => toast.error(err instanceof Error ? err.message : "Failed to generate keys"),
  });
}

export function useSendTestPush() {
  return useMutation({
    mutationFn: () => api("/admin/push/test", { method: "POST" }),
    onSuccess: () => toast.success("Test push queued — watch for a banner"),
    onError: (err) => toast.error(err instanceof Error ? err.message : "Failed to send test"),
  });
}
