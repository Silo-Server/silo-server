import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { QueryClient } from "@tanstack/react-query";
import { v2, type V2Query } from "@/api/v2/request";
import {
  fetchPolicySnapshot,
  policyDomain,
  type PolicyCompileIssue,
  type PolicySimulateRequest,
} from "@/api/adminPolicy";
import { adminKeys } from "@/hooks/queries/keys";

export type PolicyDecisionFilters = V2Query<"GET /api/v2/admin/policy/decisions">;
export interface CreatePolicyDocumentInput {
  domain: string;
  name: string;
}
export interface CreatePolicyVersionInput {
  documentId: string;
  source: string;
  comment?: string;
}
export interface ValidatePolicyInput {
  domain: string;
  source: string;
}
function invalidatePolicyDocuments(client: QueryClient, id?: string) {
  void client.invalidateQueries({ queryKey: adminKeys.policyDocuments() });
  void client.invalidateQueries({ queryKey: adminKeys.policyCapability() });
  if (id !== undefined) {
    void client.invalidateQueries({ queryKey: adminKeys.policyDocument(id) });
    void client.invalidateQueries({ queryKey: adminKeys.policyVersions(id) });
  }
}
export function usePolicyCapability() {
  return useQuery({
    queryKey: adminKeys.policyCapability(),
    queryFn: () => v2("GET /api/v2/policy/capability"),
    staleTime: 30_000,
  });
}
export function usePolicyVendor() {
  return useQuery({
    queryKey: adminKeys.policyVendor(),
    queryFn: async () => (await v2("GET /api/v2/admin/policy/vendor")).items,
    staleTime: 5 * 60_000,
  });
}
export function usePolicyDocuments() {
  const client = useQueryClient();
  const query = useInfiniteQuery({
    queryKey: adminKeys.policyDocuments(),
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) =>
      v2("GET /api/v2/admin/policy/documents", { query: { limit: 50, cursor: pageParam } }),
    getNextPageParam: (page) => (page.page?.has_more ? page.page.next_cursor : undefined),
    staleTime: 10_000,
  });
  return {
    ...query,
    data: query.data?.pages.flatMap((page) => page.items),
    restart: () => client.resetQueries({ queryKey: adminKeys.policyDocuments(), exact: true }),
  };
}
export function usePolicyDocument(id: string | undefined) {
  return useQuery({
    queryKey: adminKeys.policyDocument(id),
    queryFn: () => fetchPolicySnapshot(id!),
    enabled: id !== undefined,
    staleTime: 10_000,
  });
}
export function usePolicyVersions(id: string | undefined) {
  const client = useQueryClient();
  const query = useInfiniteQuery({
    queryKey: adminKeys.policyVersions(id),
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) =>
      v2("GET /api/v2/admin/policy/documents/{id}/versions", {
        path: { id: id! },
        query: { limit: 50, cursor: pageParam },
      }),
    getNextPageParam: (page) => (page.page?.has_more ? page.page.next_cursor : undefined),
    enabled: id !== undefined,
    staleTime: 10_000,
  });
  return {
    ...query,
    data: query.data?.pages.flatMap((page) => page.items),
    restart: () => client.resetQueries({ queryKey: adminKeys.policyVersions(id), exact: true }),
  };
}
export function usePolicyVersion(id: string | undefined, version: string | undefined) {
  return useQuery({
    queryKey: adminKeys.policyVersion(id, version),
    queryFn: () =>
      v2("GET /api/v2/admin/policy/documents/{id}/versions/{version}", {
        path: { id: id!, version: version! },
      }),
    enabled: id !== undefined && version !== undefined,
    staleTime: 10_000,
  });
}
export function usePolicyDecision(id: string | undefined) {
  return useQuery({
    queryKey: adminKeys.policyDecision(id),
    queryFn: () => v2("GET /api/v2/admin/policy/decisions/{id}", { path: { id: id! } }),
    enabled: id !== undefined,
    staleTime: 10_000,
  });
}
export function useCreatePolicyDocument() {
  const client = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (body: CreatePolicyDocumentInput) =>
      v2("POST /api/v2/admin/policy/documents", {
        body: { ...body, domain: policyDomain(body.domain) },
        retryAuthentication: false,
      }),
    onSuccess: () => invalidatePolicyDocuments(client),
  });
}
export function useCreatePolicyVersion() {
  const client = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ documentId, source, comment }: CreatePolicyVersionInput) =>
      v2("POST /api/v2/admin/policy/documents/{id}/versions", {
        path: { id: documentId },
        body: { source, comment: comment?.trim() || undefined },
        retryAuthentication: false,
      }),
    onSuccess: (data, variables) => {
      invalidatePolicyDocuments(client, variables.documentId);
      client.setQueryData(adminKeys.policyVersion(variables.documentId, data.id), data);
    },
  });
}
export function useActivatePolicyVersion() {
  const client = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({
      documentId,
      version,
      etag,
    }: {
      documentId: string;
      version: string;
      etag: string;
    }) =>
      v2("PUT /api/v2/admin/policy/documents/{id}/active-version", {
        path: { id: documentId },
        body: { version_id: version },
        headers: { "If-Match": etag },
        retryAuthentication: false,
      }),
    onSuccess: (_data, variables) => invalidatePolicyDocuments(client, variables.documentId),
  });
}
export function useSetPolicyDocumentEnabled() {
  const client = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({
      documentId,
      enabled,
      etag,
    }: {
      documentId: string;
      enabled: boolean;
      etag: string;
    }) =>
      v2("PATCH /api/v2/admin/policy/documents/{id}", {
        path: { id: documentId },
        body: { enabled },
        headers: { "If-Match": etag },
        retryAuthentication: false,
      }),
    onSuccess: (_data, variables) => invalidatePolicyDocuments(client, variables.documentId),
  });
}
export function useDeletePolicyDocument() {
  const client = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ documentId, etag }: { documentId: string; etag: string }) =>
      v2("DELETE /api/v2/admin/policy/documents/{id}", {
        path: { id: documentId },
        headers: { "If-Match": etag },
        retryAuthentication: false,
      }),
    onSuccess: () => invalidatePolicyDocuments(client),
  });
}
export function useValidatePolicy() {
  return useMutation({
    retry: false,
    mutationFn: (body: ValidatePolicyInput) =>
      v2("POST /api/v2/admin/policy/validate", {
        body: { ...body, domain: policyDomain(body.domain) },
        retryAuthentication: false,
      }),
  });
}
export function useSimulatePolicy() {
  return useMutation({
    retry: false,
    mutationFn: (body: PolicySimulateRequest) =>
      v2("POST /api/v2/admin/policy/simulate", { body, retryAuthentication: false }),
  });
}
export function usePolicyDecisions(filters: PolicyDecisionFilters, enabled = true) {
  return useQuery({
    queryKey: adminKeys.policyDecisions({ ...filters }),
    queryFn: () => v2("GET /api/v2/admin/policy/decisions", { query: filters }),
    staleTime: 5_000,
    enabled,
  });
}
export function isPolicyCompileIssue(value: unknown): value is PolicyCompileIssue {
  return (
    typeof value === "object" &&
    value !== null &&
    "message" in value &&
    typeof value.message === "string"
  );
}
