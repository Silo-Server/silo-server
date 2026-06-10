import { describe, expect, it } from "vitest";
import { buildNotification, clickUrl } from "./pushSw";

describe("buildNotification", () => {
  it("maps a full payload", () => {
    const n = buildNotification({ id: 5, title: "Approved", body: "Dune", link: "/requests" });
    expect(n.title).toBe("Approved");
    expect(n.options).toEqual({ body: "Dune", data: { link: "/requests" }, tag: "n5" });
  });
  it("falls back on missing fields", () => {
    const n = buildNotification(null);
    expect(n.title).toBe("Silo");
    expect(n.options.body).toBe("");
    expect(n.options.data.link).toBe("/notifications");
    expect(n.options.tag).toBe("n");
  });
});

describe("clickUrl", () => {
  it("uses link when present", () => expect(clickUrl({ link: "/items/9" })).toBe("/items/9"));
  it("falls back to inbox", () => expect(clickUrl({})).toBe("/notifications"));
});
