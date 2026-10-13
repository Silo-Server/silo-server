import type { RequestApprovalMode, RequestLimitMode, RequestSettings } from "@/api/types";

// Who may request, and on what terms, resolves in layers, as the server does
// it (internal/requests, EffectivePolicy): the account's own approval and
// limit, then its access group's, then the server-wide request settings. A
// layer set to inherit defers to the next, and admin accounts skip the group.
// Blocking is not a layer value: an account is blocked by the requests switch
// on the account or its group, or by requests being off server-wide. An
// account row can still hold the older "blocked" modes when an API client
// wrote them; the server honors them, and no editor offers them.

/** An approval an editor offers. */
export type RequestApprovalChoice = Exclude<RequestApprovalMode, "blocked">;
/** A limit an editor offers. */
export type RequestLimitChoice = Exclude<RequestLimitMode, "blocked">;

/** One layer's request approval and limit: an account's own, or its access group's. */
export interface RequestLimitLayer {
  limit_mode: RequestLimitMode;
  max_requests?: number | null;
  window_days?: number | null;
  approval_mode: RequestApprovalMode;
}

export type RequestServerDefaults = Pick<
  RequestSettings,
  "requests_enabled" | "global_max_requests" | "global_window_days" | "global_auto_approval_enabled"
>;

/** Where a resolved value comes from. An empty group name means the name is not known. */
export type RequestPolicySource =
  | { kind: "account" }
  | { kind: "group"; name: string }
  | { kind: "server" };

export type RequestQuota = { unlimited: true } | { unlimited: false; max: number; days: number };

/** What the layers under an account resolve to, and which layer said so. */
export interface ResolvedRequestTerms {
  quota: RequestQuota;
  quotaSource: RequestPolicySource;
  autoApprove: boolean;
  approvalSource: RequestPolicySource;
}

export type EffectiveRequestPolicy =
  | { kind: "blocked"; reason: "server-off" | "legacy" }
  | { kind: "blocked"; reason: "switch"; source: RequestPolicySource }
  | ({ kind: "allowed" } & ResolvedRequestTerms);

/** An account's access group as the resolution sees it. */
export interface RequestAccessGroup {
  name: string;
  /** The group's request approval and limit; undefined until loaded. */
  limit: RequestLimitLayer | undefined;
}

export interface RequestAccessInput {
  role: string;
  /** The account's resolved requests switch (`effective_policy.requests_allowed`). */
  requestsAllowed: boolean;
  /** The account's own requests switch; null follows its group. */
  requestsAllowedOverride: boolean | null;
  /** The account's access group, or null when it has none. */
  group: RequestAccessGroup | null;
  account: RequestLimitLayer;
  server: RequestServerDefaults;
}

const ACCOUNT: RequestPolicySource = { kind: "account" };
const SERVER: RequestPolicySource = { kind: "server" };

/** The group whose request settings apply to the account: none for an admin. */
export function requestGroupFor(
  role: string,
  group: RequestAccessGroup | null,
): RequestAccessGroup | null {
  return role === "admin" ? null : group;
}

/** The account row still holds a "blocked" mode that no editor offers any more. */
export function isLegacyRequestBlock(layer: RequestLimitLayer): boolean {
  return layer.limit_mode === "blocked" || layer.approval_mode === "blocked";
}

function serverWindowDays(server: RequestServerDefaults) {
  return server.global_window_days > 0 ? server.global_window_days : 7;
}

/**
 * Resolves approval and limit over the given layers, first one first, falling
 * back to the server-wide settings. Mirrors the server: a custom limit takes a
 * missing count or window from the server-wide one.
 */
export function resolveRequestTerms(
  layers: Array<[RequestPolicySource, RequestLimitLayer]>,
  server: RequestServerDefaults,
): ResolvedRequestTerms {
  let quota: RequestQuota = {
    unlimited: false,
    max: server.global_max_requests,
    days: serverWindowDays(server),
  };
  let quotaSource = SERVER;
  const limitLayer = layers.find(
    ([, layer]) => layer.limit_mode === "custom" || layer.limit_mode === "unlimited",
  );
  if (limitLayer) {
    const [source, layer] = limitLayer;
    quotaSource = source;
    quota =
      layer.limit_mode === "unlimited"
        ? { unlimited: true }
        : {
            unlimited: false,
            max: layer.max_requests ?? server.global_max_requests,
            days:
              layer.window_days != null && layer.window_days > 0
                ? layer.window_days
                : serverWindowDays(server),
          };
  }
  let autoApprove = server.global_auto_approval_enabled;
  let approvalSource = SERVER;
  const approvalLayer = layers.find(
    ([, layer]) => layer.approval_mode === "manual" || layer.approval_mode === "auto",
  );
  if (approvalLayer) {
    [approvalSource] = approvalLayer;
    autoApprove = approvalLayer[1].approval_mode === "auto";
  }
  return { quota, quotaSource, autoApprove, approvalSource };
}

/**
 * What applies to an account now. Undefined while the account's group limits
 * are still loading and the answer depends on them.
 */
export function effectiveRequestPolicy(
  input: RequestAccessInput,
): EffectiveRequestPolicy | undefined {
  const group = requestGroupFor(input.role, input.group);
  if (!input.server.requests_enabled) return { kind: "blocked", reason: "server-off" };
  if (!input.requestsAllowed) {
    const source: RequestPolicySource =
      input.requestsAllowedOverride !== null
        ? ACCOUNT
        : group
          ? { kind: "group", name: group.name }
          : SERVER;
    return { kind: "blocked", reason: "switch", source };
  }
  if (isLegacyRequestBlock(input.account)) return { kind: "blocked", reason: "legacy" };
  const layers: Array<[RequestPolicySource, RequestLimitLayer]> = [[ACCOUNT, input.account]];
  if (group) {
    if (!group.limit) return undefined;
    layers.push([{ kind: "group", name: group.name }, group.limit]);
  }
  return { kind: "allowed", ...resolveRequestTerms(layers, input.server) };
}

/** "Kids group", or a name that already says it, as "Default Group". */
export function requestGroupLabel(name: string): string {
  const trimmed = name.trim();
  if (!trimmed) return "its access group";
  return /\bgroup$/i.test(trimmed) ? trimmed : `${trimmed} group`;
}

function plural(n: number, one: string, many: string) {
  return n === 1 ? one : many;
}

/**
 * "5 requests per 7 days", "1 request per day", "no limit", or for a limit of
 * zero, which allows nothing new, "no new requests (0 per 7 days)".
 */
export function formatRequestQuota(quota: RequestQuota): string {
  return formatQuotaFrom(quota);
}

/** The quota, with where it comes from in the same parentheses when it has some. */
function formatQuotaFrom(quota: RequestQuota, source?: string): string {
  const from = source ? ` (${source})` : "";
  if (quota.unlimited) return `no limit${from}`;
  const window = quota.days === 1 ? "day" : `${quota.days} days`;
  if (quota.max === 0) return `no new requests (0 per ${window}${source ? `, ${source}` : ""})`;
  return `${quota.max} ${plural(quota.max, "request", "requests")} per ${window}${from}`;
}

export function formatRequestApproval(autoApprove: boolean): string {
  return autoApprove ? "approved automatically" : "an admin approves";
}

export function formatRequestSource(source: RequestPolicySource): string {
  switch (source.kind) {
    case "account":
      return "set on this account";
    case "group":
      return `from ${requestGroupLabel(source.name)}`;
    case "server":
      return "server default";
  }
}

function sourceKey(source: RequestPolicySource) {
  return source.kind === "group" ? `group:${source.name}` : source.kind;
}

/**
 * One line on what applies and where it comes from, to follow "Now: ", e.g.
 * "5 requests per 7 days, an admin approves (from Kids group)".
 */
export function describeRequestPolicy(policy: EffectiveRequestPolicy): string {
  if (policy.kind === "blocked") {
    switch (policy.reason) {
      case "server-off":
        return "can't request, because requests are turned off server-wide";
      case "legacy":
        return "can't request, because an old request setting blocks this account";
      case "switch":
        return policy.source.kind === "group"
          ? `can't request, because ${requestGroupLabel(policy.source.name)} has requests turned off`
          : "can't request, because Media Requests is off for this account";
    }
  }
  const approval = formatRequestApproval(policy.autoApprove);
  if (sourceKey(policy.quotaSource) === sourceKey(policy.approvalSource)) {
    return `${formatQuotaFrom(policy.quota)}, ${approval} (${formatRequestSource(policy.quotaSource)})`;
  }
  const quota = formatQuotaFrom(policy.quota, formatRequestSource(policy.quotaSource));
  return `${quota}, ${approval} (${formatRequestSource(policy.approvalSource)})`;
}

/**
 * What "Use the default" means for one field: the value it resolves to and
 * which layer supplies it, e.g. "Kids group: an admin approves" or
 * "Server default: 12 requests per 14 days". `group` is the account's group
 * when it has one, so an inheriting group can say it passes the server's on.
 */
export function describeInheritedValue(
  value: string,
  source: RequestPolicySource,
  group: RequestAccessGroup | null,
): string {
  if (source.kind === "group") return `${requestGroupLabel(source.name)}: ${value}`;
  if (group) return `${requestGroupLabel(group.name)} uses the server default: ${value}`;
  return `Server default: ${value}`;
}

/** A short line for an access group card, e.g. "Admin approves · 3 per 7 days". */
export function requestGroupLimitSummary(layer: RequestLimitLayer): string {
  const parts: string[] = [];
  if (layer.approval_mode === "manual") parts.push("Admin approves");
  if (layer.approval_mode === "auto") parts.push("Auto-approves");
  if (layer.limit_mode === "unlimited") parts.push("No request limit");
  if (layer.limit_mode === "custom" && layer.max_requests != null) {
    const days = layer.window_days;
    parts.push(
      days == null || days <= 0
        ? `${layer.max_requests} ${plural(layer.max_requests, "request", "requests")}`
        : `${layer.max_requests} per ${days === 1 ? "day" : `${days} days`}`,
    );
  }
  return parts.join(" · ");
}

/** An editor's staged approval and limit; the numbers stay text while typed. */
export interface RequestLimitDraft {
  approval: RequestApprovalChoice;
  limit: RequestLimitChoice;
  maxRequests: string;
  windowDays: string;
}

export function requestLimitDraft(layer: RequestLimitLayer): RequestLimitDraft {
  return {
    approval: layer.approval_mode === "blocked" ? "inherit" : layer.approval_mode,
    limit: layer.limit_mode === "blocked" ? "inherit" : layer.limit_mode,
    maxRequests: layer.max_requests == null ? "" : String(layer.max_requests),
    windowDays: layer.window_days == null ? "" : String(layer.window_days),
  };
}

/** How many staged edits differ from `base`; the numbers count only for a custom limit. */
export function requestLimitChanges(draft: RequestLimitDraft, base: RequestLimitDraft): number {
  let changes = 0;
  if (draft.approval !== base.approval) changes++;
  if (draft.limit !== base.limit) changes++;
  if (draft.limit === "custom") {
    if (draft.maxRequests !== base.maxRequests) changes++;
    if (draft.windowDays !== base.windowDays) changes++;
  }
  return changes;
}

function wholeNumber(value: string, min: number): number | null {
  const trimmed = value.trim();
  if (!/^\d+$/.test(trimmed)) return null;
  const n = Number(trimmed);
  return Number.isSafeInteger(n) && n >= min ? n : null;
}

export interface RequestLimitErrors {
  maxRequests?: string;
  windowDays?: string;
}

/**
 * A custom limit needs a whole number of requests, zero included, per at
 * least one day. The server accepts zero too: it stops new requests without
 * blocking the account (see `isZeroRequestLimit`).
 */
export function requestLimitErrors(draft: RequestLimitDraft): RequestLimitErrors {
  if (draft.limit !== "custom") return {};
  const errors: RequestLimitErrors = {};
  if (wholeNumber(draft.maxRequests, 0) === null) {
    errors.maxRequests = "Enter a whole number of requests, 0 or more.";
  }
  if (wholeNumber(draft.windowDays, 1) === null) {
    errors.windowDays = "Use at least 1 day.";
  }
  return errors;
}

/** The draft is a custom limit of zero: nothing new can be requested, which is not a block. */
export function isZeroRequestLimit(draft: RequestLimitDraft): boolean {
  return draft.limit === "custom" && wholeNumber(draft.maxRequests, 0) === 0;
}

export function hasRequestLimitErrors(errors: RequestLimitErrors): boolean {
  return Boolean(errors.maxRequests || errors.windowDays);
}

/** What an account or group limit save sends. */
export interface RequestLimitBody {
  limit_mode: RequestLimitChoice;
  approval_mode: RequestApprovalChoice;
  max_requests: number | null;
  window_days: number | null;
}

/** The body a save sends; call it only for a draft without errors. */
export function requestLimitBody(draft: RequestLimitDraft): RequestLimitBody {
  const custom = draft.limit === "custom";
  return {
    limit_mode: draft.limit,
    approval_mode: draft.approval,
    max_requests: custom ? Number(draft.maxRequests.trim()) : null,
    window_days: custom ? Number(draft.windowDays.trim()) : null,
  };
}

/** The account row with its old "blocked" modes set back to the default, the rest kept. */
export function clearLegacyRequestBlock(layer: RequestLimitLayer): RequestLimitBody {
  const limitMode = layer.limit_mode === "blocked" ? "inherit" : layer.limit_mode;
  const custom = limitMode === "custom";
  return {
    limit_mode: limitMode,
    approval_mode: layer.approval_mode === "blocked" ? "inherit" : layer.approval_mode,
    max_requests: custom ? (layer.max_requests ?? null) : null,
    window_days: custom ? (layer.window_days ?? null) : null,
  };
}
