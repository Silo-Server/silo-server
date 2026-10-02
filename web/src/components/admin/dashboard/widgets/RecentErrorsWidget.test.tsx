import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { OperationalLogEntry } from "@/api/types";

const mocks = vi.hoisted(() => ({
  useOperationalLogs: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/logs", () => ({
  useOperationalLogs: mocks.useOperationalLogs,
}));

import { RecentErrorsWidget } from "./RecentErrorsWidget";

function entry(overrides: Partial<OperationalLogEntry> = {}): OperationalLogEntry {
  return {
    id: 1,
    timestamp: new Date(Date.now() - 5 * 60_000).toISOString(),
    level: "error",
    component: "playback",
    message: "transcode session failed to start",
    ...overrides,
  };
}

function renderWidget() {
  return render(
    <MemoryRouter>
      <RecentErrorsWidget />
    </MemoryRouter>,
  );
}

describe("RecentErrorsWidget", () => {
  beforeEach(() => {
    mocks.useOperationalLogs.mockReset();
  });

  it("asks for both levels in one request", () => {
    mocks.useOperationalLogs.mockReturnValue({ data: undefined, isLoading: true, error: null });

    renderWidget();

    expect(mocks.useOperationalLogs).toHaveBeenCalledWith({ level: "error,warn", limit: 8 });
  });

  it("labels each level with a word, not only a color", () => {
    mocks.useOperationalLogs.mockReturnValue({
      data: {
        entries: [
          entry(),
          entry({ id: 2, level: "warn", component: "scanner", message: "path is unreadable" }),
        ],
      },
      isLoading: false,
      error: null,
    });

    renderWidget();

    expect(screen.getByText("Error")).toBeTruthy();
    expect(screen.getByText("Warn")).toBeTruthy();
    expect(screen.getByText("transcode session failed to start")).toBeTruthy();
    expect(screen.getByText(/playback · 5m ago/)).toBeTruthy();
    expect(screen.getByText(/scanner/)).toBeTruthy();
  });

  it("falls back to a readable label for an unexpected level", () => {
    mocks.useOperationalLogs.mockReturnValue({
      data: { entries: [entry({ level: "debug" })] },
      isLoading: false,
      error: null,
    });

    renderWidget();

    expect(screen.getByText("debug")).toBeTruthy();
  });

  it("says the log is quiet rather than rendering an empty list", () => {
    mocks.useOperationalLogs.mockReturnValue({
      data: { entries: [] },
      isLoading: false,
      error: null,
    });

    renderWidget();

    expect(screen.getByText("No errors or warnings logged.")).toBeTruthy();
  });

  it("surfaces a failed load", () => {
    mocks.useOperationalLogs.mockReturnValue({
      data: undefined,
      isLoading: false,
      error: new Error("boom"),
    });

    renderWidget();

    expect(screen.getByText("Failed to load recent errors.")).toBeTruthy();
  });
});
