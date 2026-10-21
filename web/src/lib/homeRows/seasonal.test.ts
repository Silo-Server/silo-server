import { describe, expect, it } from "vitest";
import { customHolidayProblem } from "./seasonal";

const holiday = (list_url: string) => ({ name: "Fourth", start: "07-01", end: "07-04", list_url });

describe("customHolidayProblem", () => {
  it("accepts the MDBList list links the server accepts", () => {
    expect(customHolidayProblem(holiday("https://mdblist.com/lists/a/b"))).toBeNull();
    expect(customHolidayProblem(holiday("http://www.mdblist.com:443/lists/a/b"))).toBeNull();
  });

  it("refuses links the server would refuse", () => {
    for (const link of [
      "https://example.com/lists/a/b",
      "https://user:secret@mdblist.com/lists/a/b",
      "https://@mdblist.com/lists/a/b",
      "https://:@mdblist.com/lists/a/b",
      "https:\\\\user@mdblist.com\\lists\\a",
      "https://mdblist.com:8443/lists/a/b",
      "https://mdblist.com/toplists/",
    ]) {
      expect(customHolidayProblem(holiday(link)), link).toMatch(/MDBList list link/);
    }
  });
});
