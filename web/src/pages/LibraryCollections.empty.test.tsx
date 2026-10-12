import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ isActingAdmin: false }));

vi.mock("@/hooks/useIsActingAdmin", () => ({
  useIsActingAdmin: () => mocks.isActingAdmin,
}));

vi.mock("@/hooks/queries/libraryCollections", () => ({
  useLibraryCollections: () => ({
    data: { groups: [], ungrouped: { collections: [], sort_order: 0 } },
    isLoading: false,
  }),
}));

import LibraryCollections from "./LibraryCollections";

function render() {
  return renderToStaticMarkup(
    <MemoryRouter>
      <LibraryCollections libraryId={7} />
    </MemoryRouter>,
  );
}

describe("LibraryCollections empty state", () => {
  beforeEach(() => {
    mocks.isActingAdmin = false;
  });

  it("gives regular users a plain message without admin directions", () => {
    const markup = render();

    expect(markup).toContain("No collections yet");
    expect(markup).not.toContain("admin");
    expect(markup).not.toContain("<a");
  });

  it("links admins to Admin Collections for this library", () => {
    mocks.isActingAdmin = true;

    const markup = render();

    expect(markup).toContain("No collections yet");
    expect(markup).toContain('href="/admin/collections?libraryId=7&amp;view=list"');
  });
});
