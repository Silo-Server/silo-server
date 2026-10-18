// @vitest-environment node

import { describe, expect, it } from "vitest";
import { formatExportProgressLabel } from "./adminCatalogMaintenanceFormatters";
import { createEmptyPathRewrite, updatePathRewrite } from "./adminCatalogMaintenancePathRewrites";

describe("formatExportProgressLabel", () => {
  it("adds grouping separators to export progress counts", () => {
    expect(formatExportProgressLabel(4841500, 4853125, "running")).toBe("4,841,500 / 4,853,125");
  });

  it("keeps queued exports readable when totals are not available", () => {
    expect(formatExportProgressLabel(0, 0, "queued")).toBe("Queued");
  });

  it("shows completed empty exports as waiting-free progress", () => {
    expect(formatExportProgressLabel(0, 0, "completed")).toBe("Waiting");
  });
});

describe("path rewrite helpers", () => {
  it("preserves row identity when a rewrite value changes", () => {
    const original = createEmptyPathRewrite();

    const updated = updatePathRewrite([original], 0, "from", "/srv/media");

    expect(updated).toHaveLength(1);
    expect(updated[0]).toEqual({
      id: original.id,
      from: "/srv/media",
      to: "",
    });
  });
});
