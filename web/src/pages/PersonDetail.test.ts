import { describe, it, expect } from "vitest";
import { computeAge } from "@/lib/date";

describe("computeAge", () => {
  it("computes age for a living person", () => {
    const today = new Date();
    const birthYear = today.getFullYear() - 30;
    const birthMonth = String(today.getMonth() + 1).padStart(2, "0");
    const birthDay = String(today.getDate()).padStart(2, "0");
    expect(computeAge(`${birthYear}-${birthMonth}-${birthDay}`)).toBe(30);
  });

  it("computes age at death", () => {
    expect(computeAge("1950-06-15", "2020-03-10")).toBe(69);
  });

  it("computes age at death when death is after birthday in same year", () => {
    expect(computeAge("1950-03-10", "2020-06-15")).toBe(70);
  });
});
