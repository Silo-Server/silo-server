import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Tabs } from "radix-ui";
import { describe, expect, it, vi } from "vitest";
import LibraryHeader from "./LibraryHeader";

function renderHeader(props: Partial<Parameters<typeof LibraryHeader>[0]> = {}) {
  return render(
    <Tabs.Root value="recommended">
      <LibraryHeader libraryName="Movies" libraryType="movies" {...props} />
    </Tabs.Root>,
  );
}

describe("LibraryHeader shuffle", () => {
  it("shuffles the library from the header", async () => {
    const onShuffle = vi.fn();
    renderHeader({ onShuffle });

    await userEvent.click(screen.getByRole("button", { name: "Shuffle" }));

    expect(onShuffle).toHaveBeenCalledOnce();
  });

  it("holds the button while a shuffle is starting", () => {
    renderHeader({ onShuffle: vi.fn(), shuffleDisabled: true });

    expect(screen.getByRole("button", { name: "Shuffle" })).toBeDisabled();
  });

  it("has no shuffle button for a library that cannot shuffle", () => {
    renderHeader({ libraryName: "Audiobooks", libraryType: "audiobooks" });

    expect(screen.queryByRole("button", { name: "Shuffle" })).toBeNull();
  });
});
