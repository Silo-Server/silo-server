import { useState, type ComponentProps } from "react";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { RestoreDialog } from "./RestoreDialog";

type Props = ComponentProps<typeof RestoreDialog>;

function Harness(overrides: Partial<Props>) {
  const [resetProfiles, setResetProfiles] = useState(false);
  const [restoreAll, setRestoreAll] = useState(false);
  return (
    <RestoreDialog
      open
      page={{ kind: "home" }}
      pageLabel="Home"
      otherLibraryLabels={["Movies", "TV Shows"]}
      rowCount={9}
      collectionRowTitles={[]}
      restoreAll={restoreAll}
      onRestoreAllChange={setRestoreAll}
      resetSupported
      resetProfiles={resetProfiles}
      onResetProfilesChange={setResetProfiles}
      conflict={false}
      busy={false}
      canConfirm
      onConfirm={vi.fn()}
      onReload={vi.fn()}
      onOpenChange={vi.fn()}
      {...overrides}
    />
  );
}

const bullets = () =>
  within(screen.getByRole("list", { name: "What changes" }))
    .getAllByRole("listitem")
    .map((item) => item.textContent);

afterEach(cleanup);

describe("RestoreDialog", () => {
  it("names one collection row and shortens a long list", () => {
    const { unmount } = render(
      <Harness
        page={{ kind: "library", libraryId: 7 }}
        pageLabel="Movies"
        otherLibraryLabels={[]}
        collectionRowTitles={["Studio Ghibli"]}
      />,
    );
    expect(bullets()).toEqual([
      "The 9 rows on the Movies page are replaced by Silo's default rows.",
      "Studio Ghibli shows a collection and is removed. The collection itself stays in Collections.",
      "Home doesn't change.",
    ]);
    unmount();
    render(<Harness otherLibraryLabels={[]} collectionRowTitles={["A", "B", "C", "D", "E"]} />);
    expect(bullets()).toEqual([
      "The 9 rows on Home are replaced by Silo's default rows.",
      "Rows that show collections, like A, B, C and 2 more, are removed. The collections themselves stay in Collections.",
    ]);
  });

  it("also restores every library page from Home", async () => {
    render(<Harness />);
    const toggle = screen.getByRole("switch", { name: "Also restore every library page" });
    expect(toggle).toHaveAccessibleDescription(
      "Movies, TV Shows go back to Silo's default rows too.",
    );
    await userEvent.click(toggle);
    expect(
      screen.getByRole("dialog", {
        name: "Restore Home and every library page to the default rows?",
      }),
    ).toHaveAccessibleDescription(
      "Home and every library page go back to the rows Silo starts with.",
    );
    expect(bullets()).toEqual([
      "The 9 rows on Home are replaced by Silo's default rows.",
      "Library pages (Movies, TV Shows) also go back to Silo's default rows, and their collection rows are removed.",
    ]);
    const reset = screen.getByRole("switch", {
      name: "Also reset every profile's Home and library pages",
    });
    await userEvent.click(reset);
    expect(reset).toHaveAccessibleDescription(
      "Clears what each profile hid, renamed, moved or added on Home and every library page.",
    );
  });

  it("also restores the other library pages from a library page, and leaves Home alone", async () => {
    render(
      <Harness
        page={{ kind: "library", libraryId: 7 }}
        pageLabel="Movies"
        otherLibraryLabels={["TV Shows"]}
      />,
    );
    await userEvent.click(
      screen.getByRole("switch", { name: "Also restore the other library pages" }),
    );
    expect(
      screen.getByRole("dialog", { name: "Restore every library page to the default rows?" }),
    ).toHaveAccessibleDescription("Every library page goes back to the rows Silo starts with.");
    expect(bullets()).toEqual([
      "The 9 rows on the Movies page are replaced by Silo's default rows.",
      "The other library pages (TV Shows) also go back to Silo's default rows, and their collection rows are removed. Home doesn't change.",
    ]);
    expect(
      screen.getByRole("switch", { name: "Also reset every profile's library pages" }),
    ).toBeInTheDocument();
  });

  it("turns the switch off and says why when the server can't reset profiles", () => {
    render(<Harness otherLibraryLabels={[]} resetSupported={false} />);
    const toggle = screen.getByRole("switch", { name: "Also reset every profile's Home" });
    expect(toggle).toBeDisabled();
    expect(screen.getByText("Resetting profiles isn't available on this server.")).toBeVisible();
  });

  it("confirms, and after a conflict offers only Reload rows", async () => {
    const onConfirm = vi.fn();
    const onReload = vi.fn();
    const { rerender } = render(<Harness onConfirm={onConfirm} onReload={onReload} />);
    await userEvent.click(screen.getByRole("button", { name: "Restore defaults" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);

    rerender(<Harness onConfirm={onConfirm} onReload={onReload} conflict />);
    expect(screen.getByRole("alert")).toHaveTextContent(
      "These rows changed since you opened this. Reload to see the current rows, then try again.",
    );
    expect(screen.queryByRole("button", { name: "Restore defaults" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Reload rows" }));
    expect(onReload).toHaveBeenCalledTimes(1);
  });

  it("cannot be dismissed while the restore runs", async () => {
    const onOpenChange = vi.fn();
    render(<Harness busy onOpenChange={onOpenChange} />);
    await userEvent.keyboard("{Escape}");
    expect(onOpenChange).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Restore defaults" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
  });
});
