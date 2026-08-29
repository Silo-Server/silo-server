import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { SettingsOverviewModel } from "@/hooks/admin/useSettingsOverview";
import SettingsOverview from "./SettingsOverview";

const mocks = vi.hoisted(() => ({
  useSettingsOverview: vi.fn(),
}));

vi.mock("@/hooks/admin/useSettingsOverview", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/admin/useSettingsOverview")>()),
  useSettingsOverview: () => mocks.useSettingsOverview(),
}));

function model(overrides: Partial<SettingsOverviewModel> = {}): SettingsOverviewModel {
  return {
    isLoading: false,
    tiles: [
      {
        id: "storage",
        label: "Storage",
        state: "ok",
        stateText: "Healthy",
        detail: "S3 · public + private",
      },
      {
        id: "transcoding",
        label: "Transcoding",
        state: "warn",
        stateText: "Restart pending",
        detail: "Saved changes apply after a restart",
        action: { label: "Fix", page: "playback" },
      },
      {
        id: "email",
        label: "Email",
        state: "off",
        stateText: "Not set up",
        detail: "Invites and resets can't send",
        action: { label: "Set up", page: "notifications" },
      },
    ],
    cards: [{ id: "playback" }, { id: "downloads" }, { id: "notifications" }, { id: "watch-sync" }],
    ...overrides,
  };
}

function renderOverview() {
  return render(
    <MemoryRouter initialEntries={["/admin/settings"]}>
      <SettingsOverview />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mocks.useSettingsOverview.mockReturnValue(model());
});

describe("SettingsOverview", () => {
  it("renders the page heading and nothing else above the content", () => {
    renderOverview();

    expect(screen.getByRole("heading", { level: 1, name: "Settings" })).toBeInTheDocument();
    expect(
      screen.getByText(/Configure the server, media processing, integrations/),
    ).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "Setup & health" })).toBeInTheDocument();
    expect(screen.getByText(/Recommendations and configuration problems/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "Settings groups" })).toBeInTheDocument();
    expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
  });

  it("shows only the health tiles that need something, with their action", () => {
    renderOverview();

    expect(screen.queryByTestId("overview-tile-storage")).not.toBeInTheDocument();

    const transcoding = screen.getByTestId("overview-tile-transcoding");
    expect(transcoding).toHaveAttribute("data-state", "warn");
    expect(within(transcoding).getByText("Restart pending")).toBeInTheDocument();
    expect(within(transcoding).getByRole("link")).toHaveAttribute(
      "href",
      "/admin/settings/playback",
    );

    const email = screen.getByTestId("overview-tile-email");
    expect(within(email).getByText("Set up")).toBeInTheDocument();
  });

  it("explains the empty checklist when nothing needs attention", () => {
    mocks.useSettingsOverview.mockReturnValue(
      model({ tiles: model().tiles.filter((tile) => tile.state === "ok") }),
    );
    renderOverview();

    expect(screen.getByText("No action needed")).toBeInTheDocument();
    expect(screen.getByText(/restart-required changes will appear here/)).toBeInTheDocument();
    expect(screen.queryByTestId("overview-tile-storage")).not.toBeInTheDocument();
  });

  it("renders one group card per entry with its named subareas", () => {
    renderOverview();

    const playback = screen.getByTestId("overview-card-playback");
    expect(playback).toHaveAttribute("href", "/admin/settings/playback");
    expect(within(playback).getByText("Playback")).toBeInTheDocument();
    expect(
      within(playback).getByText("Transcoding, hardware acceleration, and watch thresholds."),
    ).toBeInTheDocument();
    expect(within(playback).getByText("Transcoding")).toBeInTheDocument();
    expect(within(playback).getByText("Watch behavior")).toBeInTheDocument();
    expect(within(playback).queryByText("Current")).not.toBeInTheDocument();

    // Downloads is its own page, not a section of Playback.
    const downloads = screen.getByTestId("overview-card-downloads");
    expect(downloads).toHaveAttribute("href", "/admin/settings/downloads");
    expect(within(playback).queryByText("Downloads")).not.toBeInTheDocument();

    expect(screen.getByTestId("overview-card-watch-sync")).toHaveAttribute(
      "href",
      "/admin/settings/watch-sync",
    );
  });

  it("does not reduce a multi-setting group to one configuration summary", () => {
    renderOverview();

    const notifications = screen.getByTestId("overview-card-notifications");
    expect(within(notifications).queryByText("Email channel has no SMTP")).not.toBeInTheDocument();
  });

  it("shows skeletons instead of tiles and cards on first load", () => {
    mocks.useSettingsOverview.mockReturnValue(model({ isLoading: true }));
    renderOverview();

    expect(screen.queryByTestId("overview-tile-transcoding")).not.toBeInTheDocument();
    expect(screen.queryByTestId("overview-card-playback")).not.toBeInTheDocument();
    expect(screen.queryByText("No action needed")).not.toBeInTheDocument();
  });
});
