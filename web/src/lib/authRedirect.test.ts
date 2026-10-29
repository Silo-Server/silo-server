// @vitest-environment node

import { describe, expect, it } from "vitest";

import { guardRedirectTarget, sanitizeAuthRedirect } from "./authRedirect";

describe("sanitizeAuthRedirect", () => {
  it.each(["/", "/admin/users/7?tab=devices", "/search?q=a\\b#top", "/item/%2F%2Fx"])(
    "keeps %s",
    (path) => {
      expect(sanitizeAuthRedirect(path)).toBe(path);
    },
  );

  it.each([
    ["another site", "//evil.example"],
    ["an absolute URL", "https://evil.example/"],
    ["a backslash", "/\\evil.example"],
    ["a tab", "/\t/evil.example"],
    ["a newline", "/\n/evil.example"],
    ["a relative path", "evil.example"],
    ["nothing", ""],
  ])("drops %s", (_name, path) => {
    expect(sanitizeAuthRedirect(path)).toBeNull();
  });
});

describe("guardRedirectTarget", () => {
  it("carries the requested page, query included, as the redirect", () => {
    expect(
      guardRedirectTarget("/profiles", { pathname: "/admin/users/7", search: "?tab=devices" }),
    ).toBe("/profiles?redirect=%2Fadmin%2Fusers%2F7%3Ftab%3Ddevices");
  });

  it("adds nothing for the home page", () => {
    expect(guardRedirectTarget("/login", { pathname: "/", search: "" })).toBe("/login");
  });

  it("round-trips through the sanitizer the profile picker applies", () => {
    const target = guardRedirectTarget("/profiles", { pathname: "/admin/users/7", search: "" });
    const redirect = new URLSearchParams(target.split("?")[1]).get("redirect");
    expect(sanitizeAuthRedirect(redirect)).toBe("/admin/users/7");
  });
});
