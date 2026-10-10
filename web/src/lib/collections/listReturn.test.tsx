import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";

import { listReturnState, useListReturnPath } from "./listReturn";

function Probe() {
  return <p>{useListReturnPath("/admin/collections?view=list&libraryId=1")}</p>;
}

function renderWithState(state: unknown) {
  render(
    <MemoryRouter initialEntries={[{ pathname: "/admin/collections/c1/edit", state }]}>
      <Probe />
    </MemoryRouter>,
  );
  const text = screen.getByRole("paragraph").textContent;
  cleanup();
  return text;
}

describe("useListReturnPath", () => {
  it("returns to the exact list view the editor was opened from", () => {
    expect(renderWithState(listReturnState("/admin/collections?type=smart&q=kids&failed=1"))).toBe(
      "/admin/collections?type=smart&q=kids&failed=1",
    );
  });

  it("falls back when the editor wasn't opened from the list", () => {
    expect(renderWithState(null)).toBe("/admin/collections?view=list&libraryId=1");
  });

  it("accepts only the list's own path", () => {
    for (const returnTo of [
      "https://evil.example/admin/collections",
      "//evil.example",
      "/admin/collections/c1/edit",
      "/admin/collections?\\evil",
      42,
    ]) {
      expect(renderWithState({ returnTo })).toBe("/admin/collections?view=list&libraryId=1");
    }
  });
});
