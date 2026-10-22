import { useState } from "react";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { MoreOptions } from "./MoreOptions";

afterEach(cleanup);

/** More options over a hero row, opened, with the apps choice when `offered`. */
function renderHero(offered: boolean, initial = { hero: true, webOnly: false }) {
  function Harness() {
    const [state, setState] = useState(initial);
    return (
      <MoreOptions
        itemLimit={10}
        hero={state.hero}
        webOnly={state.webOnly}
        onItemLimitChange={() => {}}
        onHeroChange={(hero) => setState((current) => ({ ...current, hero }))}
        onWebOnlyChange={
          offered ? (webOnly) => setState((current) => ({ ...current, webOnly })) : undefined
        }
      />
    );
  }
  render(<Harness />);
  return userEvent.click(screen.getByRole("button", { name: /More options/ }));
}

describe("the hero banner's apps choice", () => {
  it("keeps a hero row off the apps, and says so on the closed box", async () => {
    await renderHero(true);
    const apps = screen.getByRole("switch", { name: "Show in the apps" });
    expect(apps).toBeChecked();
    expect(screen.getByText("The apps show this row as a regular row.")).toBeInTheDocument();
    await userEvent.click(apps);
    expect(apps).not.toBeChecked();
    expect(
      screen.getByText("Only the web shows this row. The apps don't get it."),
    ).toBeInTheDocument();
    expect(screen.getByText(/the hero banner on the web only/)).toBeInTheDocument();
  });

  it("offers the choice only while the row is the hero banner", async () => {
    await renderHero(true);
    await userEvent.click(screen.getByRole("switch", { name: "Hero banner" }));
    expect(screen.queryByRole("switch", { name: "Show in the apps" })).toBeNull();
  });

  it("keeps the apps sentence on the hero banner where the choice isn't offered", async () => {
    await renderHero(false);
    expect(screen.queryByRole("switch", { name: "Show in the apps" })).toBeNull();
    expect(
      screen.getByText("The web shows this row as a banner. The apps show it as a regular row."),
    ).toBeInTheDocument();
  });
});
