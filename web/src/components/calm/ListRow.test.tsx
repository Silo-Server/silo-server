import { useState } from "react";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Film } from "lucide-react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ListRow, MetaDot } from "./ListRow";
import { PosterPeek } from "./PosterPeek";

function Row({
  onSelect,
  grip = true,
}: {
  onSelect?: (checked: boolean, extend: boolean) => void;
  grip?: boolean;
}) {
  const [on, setOn] = useState(true);
  return (
    <ol>
      <ListRow
        id="ghibli"
        title="Studio Ghibli"
        collapsed={!on}
        collapsedText=" is hidden. Nobody sees it."
        art={<PosterPeek icon={Film} request={null} />}
        tags={<span>Pinned</span>}
        meta={
          <>
            Manual
            <MetaDot />
            23 titles
          </>
        }
        handleProps={grip ? {} : undefined}
        selection={
          onSelect
            ? { selected: false, label: "Select Studio Ghibli", onChange: onSelect }
            : undefined
        }
        shown={{ checked: on, label: "Show Studio Ghibli on Collections tabs", onChange: setOn }}
        menu={<button type="button">More for Studio Ghibli</button>}
      />
    </ol>
  );
}

afterEach(cleanup);

describe("ListRow", () => {
  it("shows the name, one tag and the meta line, with a grip named for the row", () => {
    render(<Row />);
    const row = screen.getByRole("listitem");
    expect(row).toHaveAttribute("data-row-id", "ghibli");
    expect(row).toHaveTextContent("Studio GhibliPinnedManual·23 titles");
    expect(screen.getByRole("button", { name: "Move Studio Ghibli" })).toBeInTheDocument();
  });

  it("keeps focus on the switch while the row collapses and expands", async () => {
    render(<Row />);
    const toggle = () =>
      screen.getByRole("switch", { name: "Show Studio Ghibli on Collections tabs" });
    toggle().focus();
    await userEvent.keyboard(" ");
    expect(screen.getByText(/is hidden\. Nobody sees it\./)).toBeInTheDocument();
    expect(screen.queryByText("Pinned")).not.toBeInTheDocument();
    expect(document.activeElement).toBe(toggle());
    await userEvent.keyboard(" ");
    expect(screen.queryByText(/is hidden/)).not.toBeInTheDocument();
    expect(document.activeElement).toBe(toggle());
    expect(toggle()).toBeChecked();
  });

  it("puts a checkbox in the grip's place in select mode and reports Shift for a range", async () => {
    const onSelect = vi.fn();
    render(<Row onSelect={onSelect} />);
    expect(screen.queryByRole("button", { name: "Move Studio Ghibli" })).not.toBeInTheDocument();
    const user = userEvent.setup();
    await user.keyboard("{Shift>}");
    await user.click(screen.getByRole("checkbox", { name: "Select Studio Ghibli" }));
    expect(onSelect).toHaveBeenCalledWith(true, true);
  });

  it("has no grip on a list that isn't ordered by hand", () => {
    render(<Row grip={false} />);
    expect(screen.queryByRole("button", { name: "Move Studio Ghibli" })).not.toBeInTheDocument();
    expect(screen.getByRole("listitem")).toHaveTextContent("Studio GhibliPinnedManual·23 titles");
  });
});
