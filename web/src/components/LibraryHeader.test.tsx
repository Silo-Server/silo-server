import { render, screen } from "@testing-library/react";
import { Tabs } from "radix-ui";
import { describe, expect, it } from "vitest";
import LibraryHeader from "./LibraryHeader";

function renderHeader(props: Partial<Parameters<typeof LibraryHeader>[0]> = {}) {
  return render(
    <Tabs.Root value="recommended">
      <LibraryHeader libraryName="Movies" libraryType="movies" {...props} />
    </Tabs.Root>,
  );
}

describe("LibraryHeader shuffle", () => {
  it("has no shuffle button for a library that cannot shuffle", () => {
    renderHeader({ libraryName: "Audiobooks", libraryType: "audiobooks" });

    expect(screen.queryByRole("button", { name: "Shuffle" })).toBeNull();
  });
});
