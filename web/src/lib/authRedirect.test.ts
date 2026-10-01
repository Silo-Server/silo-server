import { describe, expect, it } from "vitest";

import { guardRedirectTarget, sanitizeAuthRedirect } from "./authRedirect";

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
