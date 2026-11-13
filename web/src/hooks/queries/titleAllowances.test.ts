import { describe, expect, it } from "vitest";
import {
  allowanceDialogRows,
  allowanceRowState,
  describeMaturityLimit,
  hasAllowableProfile,
  type TitleAllowanceProfile,
} from "./titleAllowances";

function profile(id: string, state: TitleAllowanceProfile["state"]): TitleAllowanceProfile {
  return { profile_id: id, name: id, max_content_rating: "PG", max_advisory_age: null, state };
}

describe("title allowance dialog", () => {
  it("offers Allow for… only when some profile is above its limit or allowed", () => {
    expect(hasAllowableProfile(undefined)).toBe(false);
    expect(
      hasAllowableProfile([profile("a", "within_limits"), profile("b", "no_library_access")]),
    ).toBe(false);
    expect(hasAllowableProfile([profile("a", "within_limits"), profile("b", "above_limit")])).toBe(
      true,
    );
    expect(hasAllowableProfile([profile("a", "allowed_by_collection")])).toBe(true);
  });

  it("leaves out profiles that already see the title", () => {
    const rows = allowanceDialogRows([
      profile("a", "within_limits"),
      profile("b", "above_limit"),
      profile("c", "no_library_access"),
    ]);
    expect(rows.map((row) => row.profile_id)).toEqual(["b", "c"]);
  });

  it("maps each state to its checkbox", () => {
    expect(allowanceRowState("allowed")).toEqual({ checked: true, disabled: false });
    expect(allowanceRowState("above_limit")).toEqual({ checked: false, disabled: false });
    expect(allowanceRowState("allowed_by_collection")).toMatchObject({
      checked: true,
      disabled: true,
    });
    expect(allowanceRowState("no_library_access")).toMatchObject({
      checked: false,
      disabled: true,
    });
  });

  it("describes a limit from its rating and advisory age", () => {
    expect(describeMaturityLimit({ max_content_rating: "PG-13", max_advisory_age: 12 })).toBe(
      "PG-13 · Age 12+",
    );
    expect(describeMaturityLimit({ max_content_rating: "", max_advisory_age: 10 })).toBe("Age 10+");
  });
});
