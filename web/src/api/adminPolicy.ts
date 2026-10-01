import { v2, V2ProblemError, type V2Body } from "@/api/v2/request";
import type { components } from "@/api/v2/schema";

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
  throw new Error("Unsupported policy domain");
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
    throw new Error("Reload this policy before editing; its revision is unavailable.");
  return { ...value, etag };
}
export function policyMutationMessage(error: unknown, fallback: string) {
  if (error instanceof V2ProblemError && error.status === 412) {
    return "The policy changed. Your draft and original revision are kept. Review the current policy before submitting a new change.";
  }
  if (error instanceof V2ProblemError) return error.message;
  return `${fallback} The outcome may be unknown. Review the saved state before trying again.`;
}
export function policyApplyMessage(result: PolicyApplyResult) {
  const saved = `Policy change saved at generation ${result.persisted_generation}.`;
  if (!result.application.local_applied)
    return `${saved} This server could not reload it. Do not repeat the write; check the policy service before relying on the change.`;
  if (result.application.publication_failed)
    return `${saved} This server reloaded generation ${result.application.loaded_generation}, but notification failed. Other servers may still use an earlier policy.`;
  return `${saved} This server loaded generation ${result.application.loaded_generation}. Other servers apply changes independently.`;
}
