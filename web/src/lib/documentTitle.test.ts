import { describe, expect, it } from "vitest";

import {
  APP_DOCUMENT_TITLE,
  formatDocumentTitle,
  resolveAdminDocumentTitle,
  resolveSettingsDocumentTitle,
} from "./documentTitle";

describe("formatDocumentTitle", () => {
  it("returns the app name when no label is provided", () => {
    expect(formatDocumentTitle()).toBe(APP_DOCUMENT_TITLE);
    expect(formatDocumentTitle("")).toBe(APP_DOCUMENT_TITLE);
    expect(formatDocumentTitle("   ")).toBe(APP_DOCUMENT_TITLE);
  });

  it("prefixes the current page label", () => {
    expect(formatDocumentTitle("Inception")).toBe("Inception · Silo");
  });
});

describe("resolveSettingsDocumentTitle", () => {
  it("resolves nested settings route labels", () => {
    expect(resolveSettingsDocumentTitle("/settings/playback")).toBe("Playback Settings");
    expect(resolveSettingsDocumentTitle("/settings/home-screen")).toBe("Home Screen Settings");
  });

  it("falls back to the base settings title", () => {
    expect(resolveSettingsDocumentTitle("/settings")).toBe("Settings");
    expect(resolveSettingsDocumentTitle("/settings/unknown")).toBe("Settings");
  });
});

describe("resolveAdminDocumentTitle", () => {
  it("resolves major admin sections", () => {
    expect(resolveAdminDocumentTitle("/admin")).toBe("Admin");
    expect(resolveAdminDocumentTitle("/admin/access-groups")).toBe("Admin Access Groups");
    expect(resolveAdminDocumentTitle("/admin/autoscan")).toBe("Admin Autoscan");
    expect(resolveAdminDocumentTitle("/admin/collections")).toBe("Admin Collections");
    expect(resolveAdminDocumentTitle("/admin/diagnostics")).toBe("Admin Client Diagnostics");
    expect(resolveAdminDocumentTitle("/admin/policy")).toBe("Admin Policy");
    expect(resolveAdminDocumentTitle("/admin/tasks/refresh-metadata")).toBe("Admin Task");
  });

  it("handles editor routes with clearer labels", () => {
    expect(resolveAdminDocumentTitle("/admin/collections/new")).toBe("New Admin Collection");
    expect(resolveAdminDocumentTitle("/admin/collections/col-7/edit")).toBe(
      "Edit Admin Collection",
    );
  });
});
