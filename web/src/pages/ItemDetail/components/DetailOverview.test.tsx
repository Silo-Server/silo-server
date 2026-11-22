// @vitest-environment jsdom
import { afterEach, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import DetailOverview from "./DetailOverview";

afterEach(cleanup);

// A live region is announced reliably only when it exists before its content
// changes, so the status must be mounted before the translation starts and
// carry its outcome after it ends.
it("announces a translation's start and finish through one persistent status region", () => {
  const onTranslate = () => {};
  const { rerender } = render(<DetailOverview overview="Original" onTranslate={onTranslate} />);
  const status = screen.getByRole("status");
  expect(status.textContent).toBe("");

  rerender(<DetailOverview overview="Original" translating />);
  expect(screen.getByRole("status")).toBe(status);
  expect(status.textContent).toContain("Translating");

  rerender(<DetailOverview overview="Übersetzt" machineTranslated />);
  expect(screen.getByRole("status")).toBe(status);
  expect(status.textContent).toContain("Translated by AI");
});

// Moving to another item that is already AI-translated must not sound like a
// translation finishing on this page.
it("shows an existing AI translation without announcing it", () => {
  const { rerender } = render(<DetailOverview overview="Original" />);
  rerender(<DetailOverview overview="Übersetzt" machineTranslated />);
  expect(screen.getByRole("status").textContent).toBe("");
  expect(screen.getByText("Translated by AI")).toBeTruthy();
});
