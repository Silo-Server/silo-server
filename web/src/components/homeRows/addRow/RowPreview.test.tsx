import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { RowPreview } from "./RowPreview";

function preview(refreshing: boolean) {
  render(
    <RowPreview
      title="Nineties hits"
      sectionType="query"
      state={{ status: "ready", items: [{ id: "m1", title: "Heat" }], totalCount: 12, refreshing }}
      liveLabel="Live preview"
      offText="No preview"
      countUpTo={20}
    />,
  );
  return screen.getByRole("region", { name: "Preview of Nineties hits" });
}

describe("RowPreview", () => {
  it("shows how many titles a rule row matches", () => {
    const strip = preview(false);
    expect(strip).toHaveAttribute("aria-busy", "false");
    expect(screen.getByText(/titles match/)).toHaveTextContent("12 titles match·showing 12");
  });

  it("marks the match count as updating while a changed draft's preview loads", () => {
    const strip = preview(true);
    expect(strip).toHaveAttribute("aria-busy", "true");
    expect(screen.getByText(/titles match/)).toHaveTextContent(
      "Updating: 12 titles match·showing 12",
    );
  });

  it("says what an empty ready-made row looks for", () => {
    const empty = { status: "ready" as const, items: [], totalCount: 0, refreshing: false };
    const { rerender } = render(
      <RowPreview
        title="Feel-Good Comedies"
        sectionType="mood_collection"
        state={empty}
        liveLabel="Live preview"
        offText="No preview"
        rule="Comedy or Family, rated 6.5+ on TMDB."
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent(
      "No titles match right now. The row stays empty until some do.Looks for: Comedy or Family, rated 6.5+ on TMDB.",
    );
    // With titles to show, the strip shows them and no rule.
    rerender(
      <RowPreview
        title="Feel-Good Comedies"
        sectionType="mood_collection"
        state={{ ...empty, items: [{ id: "m1", title: "Paddington" }], totalCount: 1 }}
        liveLabel="Live preview"
        offText="No preview"
        rule="Comedy or Family, rated 6.5+ on TMDB."
      />,
    );
    expect(screen.queryByText(/Looks for/)).toBeNull();
  });
});
