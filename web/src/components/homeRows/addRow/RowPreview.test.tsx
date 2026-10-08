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
});
