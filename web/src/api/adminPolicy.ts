import { v2, V2ProblemError, type V2Body } from "@/api/v2/request";
import type { components } from "@/api/v2/schema";
import { t } from "@/i18n";

type Schemas = components["schemas"];
export type PolicyVendorModule = Schemas["AdminPolicyVendor"];
export type PolicyDocument = Schemas["AdminPolicySnapshot"];
export type PolicyDocumentSummary = Schemas["AdminPolicyDocument"];
export type PolicySnapshot = PolicyDocument & { etag: string };
export type PolicyVersion = Schemas["AdminPolicyVersion"];
export type PolicyVersionSummary = PolicyVersion;
export type PolicyCompileIssue = Schemas["CompileIssue"];
export type PolicyValidateResult = Schemas["AdminPolicyValidation"];
export type PolicyApplyResult = Schemas["AdminPolicyApplyResult"];
export type PolicySimulateRequest = V2Body<"POST /api/v2/admin/policy/simulate">;
export type PolicyDomain = V2Body<"POST /api/v2/admin/policy/documents">["domain"];

export function policyDomain(value: string): PolicyDomain {
  if (value === "scope" || value === "permission" || value === "action") return value;
  throw new Error(t("web.admin.common.policy.unsupportedDomain"));
}
export async function fetchPolicySnapshot(id: string): Promise<PolicySnapshot> {
  let etag = "";
  const value = await v2("GET /api/v2/admin/policy/documents/{id}", {
    path: { id },
    onResponse: (response) => {
      etag = response.headers.get("ETag") ?? "";
    },
  });
  if (!etag || etag === "*" || etag.startsWith("W/"))
    throw new Error(t("web.admin.common.policy.revisionUnavailable"));
  return { ...value, etag };
}
export function policyMutationMessage(error: unknown, fallback: string) {
  if (error instanceof V2ProblemError && error.status === 412) {
    return t("web.admin.common.policy.changedConflict");
  }
  if (error instanceof V2ProblemError) return error.message;
  return t("web.admin.common.policy.outcomeUnknown", { fallback });
}
export function policyApplyMessage(result: PolicyApplyResult) {
  const generation = result.persisted_generation;
  const loaded = result.application.loaded_generation;
  if (!result.application.local_applied)
    return t("web.admin.common.policy.savedNotReloaded", { generation });
  if (result.application.publication_failed)
    return t("web.admin.common.policy.savedNotifyFailed", { generation, loaded });
  return t("web.admin.common.policy.savedLoaded", { generation, loaded });
}
