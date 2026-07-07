import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { QueryClient } from "@tanstack/react-query";

import { api } from "@/api/client";
import type {
  PolicyActivateVersionResult,
  PolicyCapability,
  PolicyCompileIssue,
  PolicyCreateVersionResult,
  PolicyDecisionEntry,
  PolicyDecisionListResult,
  PolicyDocument,
  PolicySetDocumentEnabledResult,
  PolicySimulateRequest,
  PolicySimulateResult,
  PolicyValidateResult,
  PolicyVendorModule,
  PolicyVersion,
  PolicyVersionSummary,
} from "@/api/types";
import { adminKeys } from "@/hooks/queries/keys";

export interface PolicyDecisionFilters {
  decision_name?: string;
  user_id?: number;
  allowed?: boolean;
  from?: string;
  to?: string;
  cursor?: string;
  limit?: number;
}

export interface CreatePolicyDocumentInput {
  domain: string;
  name: string;
}

export interface CreatePolicyVersionInput {
  documentId: number;
  source: string;
  comment?: string;
}

export interface ValidatePolicyInput {
  domain: string;
  source: string;
}

function toQueryString(params: PolicyDecisionFilters) {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === "") continue;
    search.set(key, String(value));
  }
  return search.toString();
}

function invalidatePolicyDocuments(queryClient: QueryClient, id?: number) {
  const invalidations = [
    queryClient.invalidateQueries({ queryKey: adminKeys.policyDocuments() }),
    queryClient.invalidateQueries({ queryKey: adminKeys.policyCapability() }),
  ];
  if (id !== undefined) {
    invalidations.push(
      queryClient.invalidateQueries({ queryKey: adminKeys.policyDocument(id) }),
      queryClient.invalidateQueries({ queryKey: adminKeys.policyVersions(id) }),
    );
  }
  return Promise.all(invalidations);
}

export function usePolicyCapability() {
  return useQuery({
    queryKey: adminKeys.policyCapability(),
    queryFn: () => api<PolicyCapability>("/policy/capability"),
    staleTime: 30_000,
  });
}

export function usePolicyVendor() {
  return useQuery({
    queryKey: adminKeys.policyVendor(),
    queryFn: () => api<PolicyVendorModule[]>("/admin/policy/vendor"),
    staleTime: 5 * 60_000,
  });
}

export function usePolicyDocuments() {
  return useQuery({
    queryKey: adminKeys.policyDocuments(),
    queryFn: () => api<PolicyDocument[]>("/admin/policy/documents"),
    staleTime: 10_000,
  });
}

export function usePolicyDocument(id: number | undefined) {
  return useQuery({
    queryKey: adminKeys.policyDocument(id),
    queryFn: () => api<PolicyDocument>(`/admin/policy/documents/${id}`),
    enabled: id !== undefined,
    staleTime: 10_000,
  });
}

export function usePolicyVersions(id: number | undefined) {
  return useQuery({
    queryKey: adminKeys.policyVersions(id),
    queryFn: () => api<PolicyVersionSummary[]>(`/admin/policy/documents/${id}/versions`),
    enabled: id !== undefined,
    staleTime: 10_000,
  });
}

export function usePolicyVersion(id: number | undefined, version: number | undefined) {
  return useQuery({
    queryKey: adminKeys.policyVersion(id, version),
    queryFn: () => api<PolicyVersion>(`/admin/policy/documents/${id}/versions/${version}`),
    enabled: id !== undefined && version !== undefined,
    staleTime: 10_000,
  });
}

export function usePolicyDecision(id: number | undefined) {
  return useQuery({
    queryKey: adminKeys.policyDecision(id),
    queryFn: () => api<PolicyDecisionEntry>(`/admin/policy/decisions/${id}`),
    enabled: id !== undefined,
    staleTime: 10_000,
  });
}

export function useCreatePolicyDocument() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: CreatePolicyDocumentInput) =>
      api<PolicyDocument>("/admin/policy/documents", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: async () => {
      await invalidatePolicyDocuments(queryClient);
    },
  });
}

export function useCreatePolicyVersion() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ documentId, source, comment }: CreatePolicyVersionInput) =>
      api<PolicyCreateVersionResult>(`/admin/policy/documents/${documentId}/versions`, {
        method: "POST",
        body: JSON.stringify({ source, comment: comment?.trim() || undefined }),
      }),
    onSuccess: async (data, variables) => {
      await Promise.all([
        invalidatePolicyDocuments(queryClient, variables.documentId),
        queryClient.invalidateQueries({
          queryKey: adminKeys.policyVersion(variables.documentId, data.version_number),
        }),
      ]);
    },
  });
}

export function useActivatePolicyVersion() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ documentId, version }: { documentId: number; version: number }) =>
      api<PolicyActivateVersionResult>(
        `/admin/policy/documents/${documentId}/versions/${version}/activate`,
        { method: "POST" },
      ),
    onSuccess: async (_data, variables) => {
      await invalidatePolicyDocuments(queryClient, variables.documentId);
    },
  });
}

export function useSetPolicyDocumentEnabled() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ documentId, enabled }: { documentId: number; enabled: boolean }) =>
      api<PolicySetDocumentEnabledResult>(`/admin/policy/documents/${documentId}/enabled`, {
        method: "POST",
        body: JSON.stringify({ enabled }),
      }),
    onSuccess: async (_data, variables) => {
      await invalidatePolicyDocuments(queryClient, variables.documentId);
    },
  });
}

export function useDeletePolicyDocument() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (documentId: number) =>
      api<void>(`/admin/policy/documents/${documentId}`, { method: "DELETE" }),
    onSuccess: async () => {
      await invalidatePolicyDocuments(queryClient);
    },
  });
}

export function useValidatePolicy() {
  return useMutation({
    mutationFn: (body: ValidatePolicyInput) =>
      api<PolicyValidateResult>("/admin/policy/validate", {
        method: "POST",
        body: JSON.stringify(body),
      }),
  });
}

export function useSimulatePolicy() {
  return useMutation({
    mutationFn: (body: PolicySimulateRequest) =>
      api<PolicySimulateResult>("/admin/policy/simulate", {
        method: "POST",
        body: JSON.stringify(body),
      }),
  });
}

export function usePolicyDecisions(filters: PolicyDecisionFilters, enabled = true) {
  const qs = toQueryString(filters);
  return useQuery({
    queryKey: adminKeys.policyDecisions({ ...filters }),
    queryFn: () => api<PolicyDecisionListResult>(`/admin/policy/decisions${qs ? `?${qs}` : ""}`),
    staleTime: 5_000,
    enabled,
  });
}

export function isPolicyCompileIssue(value: unknown): value is PolicyCompileIssue {
  return (
    typeof value === "object" &&
    value !== null &&
    "message" in value &&
    typeof (value as { message: unknown }).message === "string"
  );
}
