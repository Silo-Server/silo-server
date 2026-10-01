import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import type { RequestDownload } from "@/api/types";
import { RequestDownloadProgress } from "./RequestDownloadProgress";

const download = (overrides: Partial<RequestDownload> = {}): RequestDownload => ({
  phase: "downloading",
  percent: 43,
  bytes_total: 4294967296,
  bytes_left: 2448131358,
  estimated_completion_at: "2026-01-02T03:16:05Z",
  downloads: 1,
  updated_at: "2026-01-02T03:03:05Z",
  ...overrides,
});

describe("RequestDownloadProgress", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-01-02T03:04:05Z"));
  });
  afterEach(() => vi.useRealTimers());

  it("draws a bar at the percentage over the label", () => {
    render(<RequestDownloadProgress download={download()} />);

    expect(screen.getByRole("progressbar", { name: "Download progress" })).toHaveAttribute(
      "aria-valuenow",
      "43",
    );
    expect(screen.getByText("Downloading · 43% · about 12 min left")).toBeInTheDocument();
  });

  it("shows only the label while the size is unknown, or the phase is new", () => {
    const { rerender } = render(
      <RequestDownloadProgress download={download({ phase: "queued", percent: undefined })} />,
    );
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
    expect(screen.getByText("Waiting to download")).toBeInTheDocument();

    rerender(<RequestDownloadProgress download={download({ phase: "verifying" })} />);
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
    expect(screen.getByText("Downloading")).toBeInTheDocument();
  });

  // Polls that return the same figures do not render the card again, so the
  // estimate keeps time on its own: it counts down, and it goes once the
  // figures are more than ten minutes old.
  it("keeps the estimate true while the figures stay the same", () => {
    vi.useRealTimers();
    vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
    vi.setSystemTime(new Date("2026-01-02T03:04:05Z"));
    render(<RequestDownloadProgress download={download()} />);
    expect(screen.getByText("Downloading · 43% · about 12 min left")).toBeInTheDocument();

    act(() => vi.advanceTimersByTime(5 * 60_000));
    expect(screen.getByText("Downloading · 43% · about 7 min left")).toBeInTheDocument();

    act(() => vi.advanceTimersByTime(6 * 60_000));
    expect(screen.getByText("Downloading · 43%")).toBeInTheDocument();
  });

  it("keeps no clock without an estimate", () => {
    vi.useRealTimers();
    vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
    vi.setSystemTime(new Date("2026-01-02T03:04:05Z"));
    const { unmount } = render(
      <RequestDownloadProgress download={download({ estimated_completion_at: undefined })} />,
    );
    expect(vi.getTimerCount()).toBe(0);
    unmount();

    render(<RequestDownloadProgress download={download()} />);
    expect(vi.getTimerCount()).toBe(1);
  });

  it("names a blocked import for admins and a wait for requesters", () => {
    const blocked = download({ phase: "import_blocked", percent: 100 });
    const { rerender } = render(<RequestDownloadProgress download={blocked} />);
    expect(screen.getByText("Waiting for import")).toBeInTheDocument();

    rerender(<RequestDownloadProgress download={blocked} admin />);
    expect(screen.getByText("Import blocked")).toBeInTheDocument();
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "100");
  });
});
