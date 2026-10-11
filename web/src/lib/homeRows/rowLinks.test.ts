import { describe, expect, it } from "vitest";
import { homeRowsPath, readRowLinks, safeReturnPath } from "./rowLinks";

const params = (query: string) => new URLSearchParams(query);

describe("readRowLinks", () => {
  it("reads a server collection to add on the admin page", () => {
    expect(readRowLinks(params("page=7&add=collection:library:lib-1"), "admin")).toEqual({
      add: { source: "library", id: "lib-1" },
      edit: null,
      returnTo: null,
    });
  });

  it("reads personal and server collections on the profile page", () => {
    expect(readRowLinks(params("add=collection:user:mine-1"), "profile")?.add).toEqual({
      source: "user",
      id: "mine-1",
    });
    expect(readRowLinks(params("add=collection:library:lib-1"), "profile")?.add).toEqual({
      source: "library",
      id: "lib-1",
    });
  });

  it("marks a personal collection on the admin page, or anything not a collection, invalid", () => {
    expect(readRowLinks(params("add=collection:user:mine-1"), "admin")?.add).toBe("invalid");
    expect(readRowLinks(params("add=trending_on_server"), "admin")?.add).toBe("invalid");
    expect(readRowLinks(params("add=collection:library:"), "admin")?.add).toBe("invalid");
    expect(readRowLinks(params("add=collection:shelf:x"), "profile")?.add).toBe("invalid");
  });

  it("reads a row to edit and a safe path to return to", () => {
    expect(readRowLinks(params("edit=row-1&return=/collections/abc/edit"), "profile")).toEqual({
      add: null,
      edit: "row-1",
      returnTo: "/collections/abc/edit",
    });
  });

  it("is null when the address holds none of the link parameters", () => {
    expect(readRowLinks(params("page=7"), "admin")).toBeNull();
  });
});

describe("safeReturnPath", () => {
  it.each([
    "/collections/abc/edit",
    "/admin/collections/abc/edit",
    "/admin/collections/abc/edit?focus=where",
  ])("honours %s", (path) => {
    expect(safeReturnPath(path)).toBe(path);
  });

  it.each([
    ["another site", "//evil.example"],
    ["an absolute URL", "https://evil.example/collections/"],
    ["a backslash", "/\\evil.example"],
    ["an encoded backslash", "/%5Cevil.example"],
    ["a lower-case encoded backslash", "/collections/%5cevil.example"],
    ["a tab", "/collections/\tx"],
    ["an encoded tab", "/collections/%09x"],
    ["an encoded newline", "/collections/%0Ax"],
    ["a newline", "/collections/\nx"],
    ["a double slash inside", "/collections//evil.example"],
    ["an encoded double slash", "/collections/%2F%2Fevil.example"],
    ["a path outside collections", "/settings"],
    ["a look-alike prefix", "/collectionsx/abc"],
    ["the bare list", "/collections"],
    ["a climb out of collections", "/collections/../settings"],
    ["an encoded climb", "/collections/%2E%2E/settings"],
    ["a malformed escape", "/collections/%E0%A4%A"],
    ["nothing", ""],
  ])("ignores %s", (_name, path) => {
    expect(safeReturnPath(path)).toBeNull();
  });

  it("ignores a missing value", () => {
    expect(safeReturnPath(null)).toBeNull();
  });
});

describe("homeRowsPath", () => {
  it("opens a row on the page it is on", () => {
    expect(homeRowsPath("admin", { kind: "home" }, { edit: "row-1" })).toBe(
      "/admin/sections?page=home&edit=row-1",
    );
    expect(homeRowsPath("profile", { kind: "library", libraryId: 7 }, { edit: "own 1" })).toBe(
      "/settings/home-screen?page=7&edit=own+1",
    );
  });
});
