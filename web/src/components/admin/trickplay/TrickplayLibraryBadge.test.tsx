import { render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { TrickplayLibraryBadge } from "./TrickplayLibraryBadge";

it("does not report ready when the library has no eligible preview files", () => {
  render(
    <TrickplayLibraryBadge
      library={{
        library_id: "1",
        name: "Movies",
        pending: 0,
        running: 0,
        ready: 0,
        unusable: 0,
        sheet_bytes: 0,
      }}
    />,
  );
  expect(screen.queryByText("Previews ready")).toBeNull();
  expect(screen.getByText("Previews 0%")).toBeTruthy();
});
