import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { RestartBanner } from "./RestartBanner";

describe("RestartBanner", () => {
  it("stays hidden until a restart is owed", () => {
    const { container } = render(<RestartBanner />);

    expect(container).toBeEmptyDOMElement();
  });

  it("prompts for a restart and can be deferred", async () => {
    render(<RestartBanner restartRequired description="FFmpeg path takes effect on restart." />);

    expect(screen.getByText("Restart required")).toBeInTheDocument();
    expect(screen.getByText("FFmpeg path takes effect on restart.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Restart server/ })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Later" }));
    expect(screen.queryByText("Restart required")).not.toBeInTheDocument();
  });

  it("comes back when a new restart reason arrives", async () => {
    const { rerender } = render(<RestartBanner restartRequired />);

    await userEvent.click(screen.getByRole("button", { name: "Later" }));
    expect(screen.queryByText("Restart required")).not.toBeInTheDocument();

    rerender(<RestartBanner restartRequired={false} />);
    rerender(<RestartBanner restartRequired />);
    expect(screen.getByText("Restart required")).toBeInTheDocument();
  });

  it("comes back when a later save bumps the mark count while the flag stays latched", async () => {
    // The server's restart_required boolean never clears while the process
    // lives, so a second restart-required save is only visible as a bumped
    // restart_mark_count.
    const { rerender } = render(<RestartBanner restartRequired restartSignal={1} />);

    await userEvent.click(screen.getByRole("button", { name: "Later" }));
    expect(screen.queryByText("Restart required")).not.toBeInTheDocument();

    rerender(<RestartBanner restartRequired restartSignal={2} />);
    expect(screen.getByText("Restart required")).toBeInTheDocument();

    // Dismissing the new signal holds again until something newer arrives.
    await userEvent.click(screen.getByRole("button", { name: "Later" }));
    rerender(<RestartBanner restartRequired restartSignal={2} />);
    expect(screen.queryByText("Restart required")).not.toBeInTheDocument();
  });

  it("sits in the page flow instead of covering it", () => {
    render(<RestartBanner restartRequired />);

    // The bottom-pinned version had to dodge the sidebar, reserve scroll room
    // and lift the save pill. An in-flow block needs none of that, so a
    // regression back to `fixed` should fail here.
    const banner = screen.getByRole("status");
    expect(banner.className).not.toMatch(/\bfixed\b/);
    expect(document.documentElement.style.getPropertyValue("--settings-dock-offset")).toBe("");
  });
});
