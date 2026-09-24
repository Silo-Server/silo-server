import { render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { MetadataActionDialogHost } from "./MediaItemMenu";

// The Edit Metadata chunk fails to load, the way a chunk from a previous deploy
// does once the server has moved on.
vi.mock("@/components/EditMetadataDialog", () => {
  throw new Error("Failed to fetch dynamically imported module");
});

vi.mock("@/hooks/queries/catalogRead", () => ({
  useCatalogItemDetail: () => ({ data: { content_id: "series-1", title: "Silo" } }),
}));

it("keeps a failed dialog chunk inside the host and offers a reload", async () => {
  const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
  try {
    render(
      <main>
        <p>page</p>
        <MetadataActionDialogHost action="edit" contentId="series-1" onClose={() => undefined} />
      </main>,
    );

    expect(await screen.findByText("The dialog could not be loaded.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload Page" })).toBeInTheDocument();
    expect(screen.getByText("page")).toBeInTheDocument();
  } finally {
    consoleError.mockRestore();
  }
});
