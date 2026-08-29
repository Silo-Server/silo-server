import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { ProviderTile, ProviderTileGrid } from "@/components/settings/ProviderTile";

describe("ProviderTileGrid", () => {
  it("stops at two columns so tile headers do not truncate the provider name", () => {
    const { container } = render(
      <ProviderTileGrid>
        <ProviderTile name="OpenSubtitles" state="connected" />
      </ProviderTileGrid>,
    );

    const grid = container.firstElementChild;
    expect(grid).toHaveClass("sm:grid-cols-2");
    expect(grid?.className).not.toMatch(/grid-cols-3/);
  });
});

describe("ProviderTile", () => {
  it("names the tile after the provider so it can be found as a group", () => {
    render(<ProviderTile name="OpenSubtitles" tagline="Community subtitles" state="connected" />);

    expect(screen.getByRole("group", { name: "OpenSubtitles" })).toBeInTheDocument();
    expect(screen.getByText("Community subtitles")).toBeInTheDocument();
  });

  it("labels each connection state and exposes it for styling", () => {
    const { rerender } = render(<ProviderTile name="SubDL" state="connected" />);
    expect(screen.getByText("Connected")).toBeInTheDocument();

    rerender(<ProviderTile name="SubDL" state="not_connected" />);
    expect(screen.getByText("Not connected")).toBeInTheDocument();

    rerender(<ProviderTile name="SubDL" state="error" meta="401 — key rejected 2h ago" />);
    expect(screen.getByText("Error")).toBeInTheDocument();
    expect(screen.getByText("401 — key rejected 2h ago")).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "SubDL" })).toHaveAttribute("data-state", "error");
  });

  it("takes a pill text override", () => {
    render(<ProviderTile name="TheTVDB" state="connected" statePill="Subscriber key" />);

    expect(screen.getByText("Subscriber key")).toBeInTheDocument();
    expect(screen.queryByText("Connected")).not.toBeInTheDocument();
  });

  it("runs the tile's own action", async () => {
    const onClick = vi.fn();
    render(
      <ProviderTile
        name="SubSource"
        state="not_connected"
        primaryAction={{ label: "Connect", onClick }}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Connect" }));
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("hides the panel until the tile is expanded", () => {
    const { rerender } = render(
      <ProviderTile
        name="TMDB"
        state="connected"
        primaryAction={{ label: "Manage", onClick: vi.fn() }}
      >
        <label>
          API key
          <input />
        </label>
      </ProviderTile>,
    );

    expect(screen.queryByLabelText("API key")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Manage" })).toBeInTheDocument();

    rerender(
      <ProviderTile
        name="TMDB"
        state="editing"
        expanded
        primaryAction={{ label: "Manage", onClick: vi.fn() }}
      >
        <label>
          API key
          <input />
        </label>
      </ProviderTile>,
    );

    expect(screen.getByLabelText("API key")).toBeInTheDocument();
    // The tile's own button gives way to the panel's action row.
    expect(screen.queryByRole("button", { name: "Manage" })).not.toBeInTheDocument();
    expect(screen.getByRole("group", { name: "TMDB" })).toHaveAttribute("data-expanded", "true");
  });

  it("disables everything inside while busy", () => {
    render(
      <ProviderTile
        name="TMDB"
        state="connected"
        busy
        primaryAction={{ label: "Manage", onClick: vi.fn() }}
      />,
    );

    expect(screen.getByRole("button", { name: "Manage" })).toBeDisabled();
  });
});
