import { describe, expect, it } from "vitest";

import {
  clearLegacyRequestBlock,
  describeInheritedValue,
  describeRequestPolicy,
  effectiveRequestPolicy,
  formatRequestQuota,
  isZeroRequestLimit,
  requestGroupLabel,
  requestGroupLimitSummary,
  requestLimitBody,
  requestLimitChanges,
  requestLimitDraft,
  requestLimitErrors,
  resolveRequestTerms,
  type RequestAccessInput,
  type RequestLimitLayer,
} from "./requestAccess";

const SERVER = {
  requests_enabled: true,
  global_max_requests: 12,
  global_window_days: 14,
  global_auto_approval_enabled: true,
};
const INHERIT: RequestLimitLayer = { limit_mode: "inherit", approval_mode: "inherit" };
const KIDS: RequestLimitLayer = {
  limit_mode: "custom",
  max_requests: 5,
  window_days: 7,
  approval_mode: "manual",
};

function input(overrides: Partial<RequestAccessInput> = {}): RequestAccessInput {
  return {
    role: "user",
    requestsAllowed: true,
    requestsAllowedOverride: null,
    group: { name: "Kids", limit: KIDS },
    account: INHERIT,
    server: SERVER,
    ...overrides,
  };
}
const now = (overrides: Partial<RequestAccessInput> = {}) => {
  const policy = effectiveRequestPolicy(input(overrides));
  return policy && describeRequestPolicy(policy);
};

describe("effective request policy", () => {
  it("takes the group's approval and limit when the account inherits", () => {
    expect(now()).toBe("5 requests per 7 days, an admin approves (from Kids group)");
  });

  it("lets the account's own setting win over its group's, field by field", () => {
    expect(now({ account: { limit_mode: "unlimited", approval_mode: "inherit" } })).toBe(
      "no limit (set on this account), an admin approves (from Kids group)",
    );
    expect(
      now({
        account: { limit_mode: "custom", max_requests: 1, window_days: 1, approval_mode: "auto" },
      }),
    ).toBe("1 request per day, approved automatically (set on this account)");
  });

  it("falls back to the server-wide settings when the group inherits too", () => {
    expect(now({ group: { name: "Default Group", limit: INHERIT } })).toBe(
      "12 requests per 14 days, approved automatically (server default)",
    );
    expect(
      now({ group: { name: "Kids", limit: { limit_mode: "inherit", approval_mode: "manual" } } }),
    ).toBe("12 requests per 14 days (server default), an admin approves (from Kids group)");
  });

  it("uses the server's window when a custom limit has none, and 7 days when the server has none", () => {
    const terms = resolveRequestTerms(
      [[{ kind: "account" }, { limit_mode: "custom", max_requests: 3, approval_mode: "inherit" }]],
      { ...SERVER, global_window_days: 0 },
    );
    expect(terms.quota).toEqual({ unlimited: false, max: 3, days: 7 });
  });

  it("skips the group for an admin account", () => {
    expect(now({ role: "admin" })).toBe(
      "12 requests per 14 days, approved automatically (server default)",
    );
    // An admin needs no group limits to resolve, even while they load.
    expect(now({ role: "admin", group: { name: "Kids", limit: undefined } })).toBe(
      "12 requests per 14 days, approved automatically (server default)",
    );
  });

  it("waits for the group's limits when the answer depends on them", () => {
    expect(effectiveRequestPolicy(input({ group: { name: "Kids", limit: undefined } }))).toBe(
      undefined,
    );
    expect(now({ group: null })).toBe(
      "12 requests per 14 days, approved automatically (server default)",
    );
  });

  it("says why an account can't request, server-wide off first", () => {
    expect(now({ server: { ...SERVER, requests_enabled: false }, requestsAllowed: false })).toBe(
      "can't request, because requests are turned off server-wide",
    );
    expect(now({ requestsAllowed: false, requestsAllowedOverride: false })).toBe(
      "can't request, because Media Requests is off for this account",
    );
    expect(now({ requestsAllowed: false })).toBe(
      "can't request, because Kids group has requests turned off",
    );
    expect(now({ account: { limit_mode: "blocked", approval_mode: "auto" } })).toBe(
      "can't request, because an old request setting blocks this account",
    );
    expect(now({ account: { limit_mode: "inherit", approval_mode: "blocked" } })).toBe(
      "can't request, because an old request setting blocks this account",
    );
  });
});

describe("request access wording", () => {
  it("names groups without doubling the word", () => {
    expect(requestGroupLabel("Kids")).toBe("Kids group");
    expect(requestGroupLabel("Default Group")).toBe("Default Group");
    expect(requestGroupLabel("")).toBe("its access group");
  });

  it("formats quotas", () => {
    expect(formatRequestQuota({ unlimited: true })).toBe("no limit");
    expect(formatRequestQuota({ unlimited: false, max: 2, days: 30 })).toBe(
      "2 requests per 30 days",
    );
    expect(formatRequestQuota({ unlimited: false, max: 0, days: 7 })).toBe(
      "no new requests (0 per 7 days)",
    );
  });

  it("reads naturally for a limit of zero", () => {
    expect(
      now({
        account: {
          limit_mode: "custom",
          max_requests: 0,
          window_days: 1,
          approval_mode: "inherit",
        },
      }),
    ).toBe("no new requests (0 per day, set on this account), an admin approves (from Kids group)");
    expect(now({ group: { name: "Kids", limit: { ...KIDS, max_requests: 0 } } })).toBe(
      "no new requests (0 per 7 days), an admin approves (from Kids group)",
    );
  });

  it("says where an inherited value comes from", () => {
    const kids = { name: "Kids", limit: INHERIT };
    expect(describeInheritedValue("x", { kind: "group", name: "Kids" }, kids)).toBe(
      "Kids group: x",
    );
    expect(describeInheritedValue("x", { kind: "server" }, kids)).toBe(
      "Kids group uses the server default: x",
    );
    expect(describeInheritedValue("x", { kind: "server" }, null)).toBe("Server default: x");
  });

  it("summarizes a group's own terms for its card", () => {
    expect(requestGroupLimitSummary(KIDS)).toBe("Admin approves · 5 per 7 days");
    expect(requestGroupLimitSummary({ limit_mode: "unlimited", approval_mode: "auto" })).toBe(
      "Auto-approves · No request limit",
    );
    expect(requestGroupLimitSummary(INHERIT)).toBe("");
  });
});

describe("request limit drafts", () => {
  it("round-trips a layer and counts edits", () => {
    const base = requestLimitDraft(KIDS);
    expect(base).toEqual({
      approval: "manual",
      limit: "custom",
      maxRequests: "5",
      windowDays: "7",
    });
    expect(requestLimitChanges(base, base)).toBe(0);
    expect(requestLimitChanges({ ...base, maxRequests: "6" }, base)).toBe(1);
    // The numbers stop counting once the limit is no longer custom.
    expect(requestLimitChanges({ ...base, limit: "inherit", maxRequests: "6" }, base)).toBe(1);
    expect(requestLimitBody({ ...base, maxRequests: " 6 " })).toEqual({
      limit_mode: "custom",
      approval_mode: "manual",
      max_requests: 6,
      window_days: 7,
    });
    expect(requestLimitBody({ ...base, limit: "unlimited" })).toMatchObject({
      max_requests: null,
      window_days: null,
    });
  });

  it("never offers blocked, accepts zero requests, and asks for at least one day", () => {
    expect(requestLimitDraft({ limit_mode: "blocked", approval_mode: "blocked" })).toMatchObject({
      limit: "inherit",
      approval: "inherit",
    });
    const draft = requestLimitDraft(KIDS);
    expect(requestLimitErrors(draft)).toEqual({});
    // Zero is valid, as it is on the server: it stops new requests.
    const zero = { ...draft, maxRequests: "0" };
    expect(requestLimitErrors(zero)).toEqual({});
    expect(isZeroRequestLimit(zero)).toBe(true);
    expect(isZeroRequestLimit(draft)).toBe(false);
    expect(isZeroRequestLimit({ ...zero, limit: "inherit" })).toBe(false);
    expect(requestLimitBody(zero)).toMatchObject({ max_requests: 0, window_days: 7 });
    expect(requestLimitErrors({ ...draft, maxRequests: "-1", windowDays: "0" })).toEqual({
      maxRequests: "Enter a whole number of requests, 0 or more.",
      windowDays: "Use at least 1 day.",
    });
    expect(requestLimitErrors({ ...draft, maxRequests: "1.5", windowDays: "1.5" })).toEqual({
      maxRequests: "Enter a whole number of requests, 0 or more.",
      windowDays: "Use at least 1 day.",
    });
    expect(requestLimitErrors({ ...draft, limit: "inherit", maxRequests: "" })).toEqual({});
  });

  it("clears only the old blocked modes of a legacy row", () => {
    expect(
      clearLegacyRequestBlock({ limit_mode: "blocked", max_requests: 3, approval_mode: "auto" }),
    ).toEqual({
      limit_mode: "inherit",
      approval_mode: "auto",
      max_requests: null,
      window_days: null,
    });
    expect(
      clearLegacyRequestBlock({
        limit_mode: "custom",
        max_requests: 3,
        window_days: 5,
        approval_mode: "blocked",
      }),
    ).toEqual({ limit_mode: "custom", approval_mode: "inherit", max_requests: 3, window_days: 5 });
  });
});
