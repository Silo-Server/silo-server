import { describe, expect, it } from "vitest";
import {
  accessExplanationReasonLabel,
  accessExplanationSourceLabel,
  formatPolicyValue,
  policyLimitRows,
} from "./adminAccessExplainModel";

describe("admin access explanation model", () => {
  it("labels cascade sources for admins", () => {
    expect(accessExplanationSourceLabel({ type: "group", id: "standard_user", name: "User" })).toBe(
      "Group: User",
    );
    expect(accessExplanationSourceLabel({ type: "builtin_role", id: "admin", name: "Admin" })).toBe(
      "Built-in role: Admin",
    );
    expect(accessExplanationSourceLabel({ type: "default", name: "Default deny" })).toBe(
      "Default deny",
    );
  });

  it("labels access decision reasons", () => {
    expect(
      accessExplanationReasonLabel({
        action: "playback.play",
        resource_type: "media_item",
        allowed: true,
        reason_code: "rule_allow",
        source: { type: "group", name: "User" },
        matched_rules: [],
        evaluated_rules: [],
      }),
    ).toBe("Allowed by rule");
    expect(
      accessExplanationReasonLabel({
        action: "security.manage",
        resource_type: "security_settings",
        allowed: false,
        reason_code: "default_deny",
        source: { type: "default" },
        matched_rules: [],
        evaluated_rules: [],
      }),
    ).toBe("No matching allow");
  });

  it("formats effective policy rows", () => {
    expect(formatPolicyValue(["movie", "ebook"])).toBe("movie, ebook");
    expect(formatPolicyValue([])).toBe("All");
    expect(formatPolicyValue(true)).toBe("Allowed");
    expect(formatPolicyValue("")).toBe("Inherited");

    const rows = policyLimitRows({
      max_streams: 4,
      max_transcodes: 2,
      max_profiles: 5,
      direct_downloads_allowed: true,
      transcoded_downloads_allowed: false,
    });

    expect(rows).toContainEqual(["Max Streams", "4"]);
    expect(rows).toContainEqual(["Direct Downloads", "Allowed"]);
    expect(rows).toContainEqual(["Transcoded Downloads", "Not allowed"]);
  });
});
