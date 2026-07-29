import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { api, apiResponse } from "@/api/client";
import type {
  AdminSettingUpdateResponse,
  DiagnosticReport,
  DiagnosticReportListResponse,
  DiagnosticReportSummary,
  DiagnosticStatus,
} from "@/api/types";
import { adminKeys } from "@/hooks/queries/keys";

export interface AdminDiagnosticsQuery {
  user_id?: number | string;
  platform?: string;
  report_type?: string;
  from?: string;
  to?: string;
  short_id?: string;
  limit?: number;
  cursor?: string;
}

function toQueryString(params: AdminDiagnosticsQuery) {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === "") continue;
    search.set(key, String(value));
  }
  return search.toString();
}

export function useDiagnosticsStatus() {
  return useQuery({
    queryKey: adminKeys.diagnosticStatus(),
    queryFn: () => api<DiagnosticStatus>("/diagnostics/status"),
    staleTime: 30_000,
  });
}

export function useUpdateDiagnosticsUploadsEnabled() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (enabled: boolean) =>
      api<AdminSettingUpdateResponse>("/admin/settings/diagnostics.uploads_enabled", {
        method: "PUT",
        body: JSON.stringify({ value: enabled ? "true" : "false" }),
      }),
    onSuccess: async (_result, enabled) => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: adminKeys.diagnosticStatus() }),
        queryClient.invalidateQueries({ queryKey: adminKeys.serverSettings() }),
      ]);
      toast.success(
        enabled ? "Client diagnostic uploads enabled" : "Client diagnostic uploads disabled",
      );
    },
    onError: (error) => {
      toast.error(
        error instanceof Error ? error.message : "Failed to update client diagnostic uploads",
      );
    },
  });
}

export function useDiagnosticReports(params: AdminDiagnosticsQuery) {
  const query = toQueryString(params);
  return useQuery({
    queryKey: adminKeys.diagnosticReports({ ...params }),
    queryFn: () =>
      api<DiagnosticReportListResponse>(`/admin/diagnostics/reports${query ? `?${query}` : ""}`),
    staleTime: 5_000,
  });
}

export function useDiagnosticReport(id?: string) {
  return useQuery({
    queryKey: adminKeys.diagnosticReport(id),
    queryFn: () => api<DiagnosticReport>(`/admin/diagnostics/reports/${encodeURIComponent(id!)}`),
    enabled: Boolean(id),
  });
}

export function useDeleteDiagnosticReport() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      api<void>(`/admin/diagnostics/reports/${encodeURIComponent(id)}`, {
        method: "DELETE",
      }),
    onSuccess: (_result, id) => {
      queryClient.removeQueries({ queryKey: adminKeys.diagnosticReport(id) });
      void queryClient.invalidateQueries({
        queryKey: ["admin", "diagnostics", "reports"],
      });
      toast.success("Diagnostic report deleted");
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to delete diagnostic report");
    },
  });
}

export async function downloadDiagnosticReport(report: DiagnosticReportSummary) {
  // Always stream the bundle through the server (proxy mode) instead of
  // following a presigned URL. When S3Private points at an endpoint only the
  // server can reach (an internal MinIO/R2 gateway), a presigned URL sends the
  // browser to an unreachable host, and because that navigation happens in a
  // separate window the UI can neither detect the failure nor fall back. Admin
  // downloads are bounded and rare, so proxying through the server is reliable
  // and lets errors surface here for the caller to report.
  const response = await apiResponse(
    `/admin/diagnostics/reports/${encodeURIComponent(report.id)}/download?proxy=1`,
  );
  const blob = await response.blob();
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = `silo-diagnostics-${report.short_id || report.id}.tar.gz`;
  anchor.style.display = "none";
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 0);
}
