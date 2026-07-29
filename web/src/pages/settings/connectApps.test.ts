import { describe, expect, it } from "vitest";

import { buildJellyfinUsername, isLoopbackURL, jellyfinUsernameIssue } from "./connectApps";

describe("buildJellyfinUsername", () => {
  it("joins the account and profile with the separator the resolver expects", () => {
    expect(buildJellyfinUsername("johndoe", "Kids")).toBe("johndoe#Kids");
  });

  it("preserves internal spaces, which the resolver matches verbatim", () => {
    expect(buildJellyfinUsername(" johndoe ", " Doe Household ")).toBe("johndoe#Doe Household");
  });

  it("falls back to the bare account when there is no profile to append", () => {
    expect(buildJellyfinUsername("johndoe", "   ")).toBe("johndoe");
  });
});

describe("jellyfinUsernameIssue", () => {
  it("rejects names containing the separator the resolver splits on", () => {
    expect(jellyfinUsernameIssue("Movie #2")).toContain("contains a #");
  });

  it("accepts ordinary names", () => {
    expect(jellyfinUsernameIssue("Doe Household")).toBeNull();
  });
});

describe("isLoopbackURL", () => {
  it.each([
    "http://127.0.0.1:8096",
    "http://127.1.2.3:8096",
    "http://localhost:8096",
    "https://silo.localhost",
    "http://0.0.0.0:8096",
    "http://[::1]:8096",
  ])("treats %s as unreachable from another device", (url) => {
    expect(isLoopbackURL(url)).toBe(true);
  });

  it.each(["https://compat.example.test", "http://192.168.1.10:8096", "https://10.0.0.5"])(
    "treats %s as a real address",
    (url) => {
      expect(isLoopbackURL(url)).toBe(false);
    },
  );

  it("does not flag a hostname that merely contains a loopback substring", () => {
    expect(isLoopbackURL("https://localhost.example.com")).toBe(false);
    expect(isLoopbackURL("https://not-127.0.0.1.example.com")).toBe(false);
  });

  it("returns false for unparseable input rather than throwing", () => {
    expect(isLoopbackURL("")).toBe(false);
    expect(isLoopbackURL("not a url")).toBe(false);
  });
});
