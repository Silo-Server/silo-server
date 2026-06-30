import type {
  AdminACLActionExplanation,
  AdminACLExplanationSource,
  AdminEffectivePolicy,
} from "@/api/types";

export function accessExplanationSourceLabel(source: AdminACLExplanationSource) {
  switch (source.type) {
    case "group":
      return source.name ? `Group: ${source.name}` : "Group";
    case "builtin_role":
      return source.name ? `Built-in role: ${source.name}` : "Built-in role";
    case "legacy_permission":
      return source.name || "Legacy permission";
    case "user":
      return source.name || "Direct user grant";
    case "everyone":
      return "Everyone";
    case "default":
      return "Default deny";
    default:
      return source.name || source.id || source.type;
  }
}

export function accessExplanationReasonLabel(row: AdminACLActionExplanation) {
  switch (row.reason_code) {
    case "rule_allow":
      return "Allowed by rule";
    case "rule_deny":
      return "Denied by rule";
    case "default_deny":
      return "No matching allow";
    case "user_disabled":
      return "User disabled";
    default:
      return row.reason_code;
  }
}

export function formatPolicyValue(value: unknown) {
  if (Array.isArray(value)) {
    return value.length > 0 ? value.join(", ") : "All";
  }
  if (typeof value === "boolean") {
    return value ? "Allowed" : "Not allowed";
  }
  if (value === "" || value == null) {
    return "Inherited";
  }
  return String(value);
}

export function policyLimitRows(policy: AdminEffectivePolicy) {
  return [
    ["Libraries", formatPolicyValue(policy.library_ids)],
    ["Media Types", formatPolicyValue(policy.media_types)],
    ["Max Quality", formatPolicyValue(policy.max_playback_quality)],
    ["Max Streams", formatPolicyValue(policy.max_streams)],
    ["Max Transcodes", formatPolicyValue(policy.max_transcodes)],
    ["Max Profiles", formatPolicyValue(policy.max_profiles)],
    ["Direct Downloads", formatPolicyValue(policy.direct_downloads_allowed)],
    ["Transcoded Downloads", formatPolicyValue(policy.transcoded_downloads_allowed)],
  ] as const;
}
