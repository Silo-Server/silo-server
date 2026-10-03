import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  isActingAdmin: false,
  activeScans: [] as Array<{ id: string; library_id: number; status: string }>,
}));

vi.mock("@/hooks/useIsActingAdmin", () => ({
  useIsActingAdmin: () => mocks.isActingAdmin,
}));

vi.mock("@/hooks/queries/admin/scans", () => ({
  useActiveScans: () => ({ data: mocks.activeScans }),
}));

vi.mock("@/hooks/queries/admin/scanControls", () => ({
  useScanLibrary: () => ({ isPending: false, mutate: vi.fn() }),
}));

import LibraryEmptyState from "./LibraryEmptyState";

function render() {
  return renderToStaticMarkup(
    <MemoryRouter>
      <LibraryEmptyState libraryId={7} />
    </MemoryRouter>,
  );
}

describe("LibraryEmptyState", () => {
  beforeEach(() => {
    mocks.isActingAdmin = false;
    mocks.activeScans = [];
  });

  it("gives regular users a plain message without admin actions", () => {
    const markup = render();

    expect(markup).toContain("This library is empty");
    expect(markup).toContain("There is nothing in this library yet.");
    expect(markup).not.toContain("Scan library");
    expect(markup).not.toContain("/admin/libraries");
  });

  it("points admins at scanning and library management", () => {
    mocks.isActingAdmin = true;

    const markup = render();

    expect(markup).toContain("This library is empty");
    expect(markup).toContain("Scan library");
    expect(markup).toContain('href="/admin/libraries"');
  });

  it("tells admins a scan is running instead of calling the library empty", () => {
    mocks.isActingAdmin = true;
    mocks.activeScans = [{ id: "scan-1", library_id: 7, status: "running" }];

    const markup = render();

    expect(markup).toContain("Scanning this library");
    expect(markup).not.toContain("This library is empty");
  });

  it("ignores scans of other libraries", () => {
    mocks.isActingAdmin = true;
    mocks.activeScans = [{ id: "scan-1", library_id: 8, status: "running" }];

    expect(render()).toContain("This library is empty");
  });
});
