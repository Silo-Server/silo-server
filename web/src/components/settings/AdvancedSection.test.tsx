import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";

import { AdvancedSection } from "@/components/settings/AdvancedSection";

function renderSection(props: Partial<Parameters<typeof AdvancedSection>[0]> = {}) {
  return render(
    <AdvancedSection id="playback.transcoding" count={3} {...props}>
      <div>ffmpeg path</div>
    </AdvancedSection>,
  );
}

describe("AdvancedSection", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("starts collapsed and labels the disclosure with the setting count", () => {
    renderSection();

    const toggle = screen.getByRole("button", { name: /Advanced · 3 settings/ });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("ffmpeg path")).not.toBeInTheDocument();
  });

  it("uses the singular form for a single setting", () => {
    renderSection({ count: 1 });

    expect(screen.getByRole("button", { name: /Advanced · 1 setting$/ })).toBeInTheDocument();
  });

  it("shows the count as a bare number and spells it out for screen readers", () => {
    renderSection({ count: 9 });

    expect(screen.getByRole("button", { name: "Advanced · 9 settings" })).toBeInTheDocument();
    expect(screen.getByText("9")).toBeInTheDocument();
  });

  it("persists the open state under the section id", async () => {
    const user = userEvent.setup();
    const { unmount } = renderSection();

    await user.click(screen.getByRole("button", { name: /Advanced/ }));
    expect(screen.getByText("ffmpeg path")).toBeInTheDocument();
    expect(localStorage.getItem("silo.admin.advanced.playback.transcoding")).toBe("true");

    unmount();
    renderSection();
    expect(screen.getByText("ffmpeg path")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /Advanced/ }));
    expect(localStorage.getItem("silo.admin.advanced.playback.transcoding")).toBe("false");
    expect(screen.queryByText("ffmpeg path")).not.toBeInTheDocument();
  });

  it("does not inherit another section's persisted state", () => {
    localStorage.setItem("silo.admin.advanced.playback.transcoding", "true");
    renderSection({ id: "downloads" });

    expect(screen.queryByText("ffmpeg path")).not.toBeInTheDocument();
  });

  it("honours defaultOpen only until a choice is persisted", async () => {
    const user = userEvent.setup();
    const { unmount } = renderSection({ defaultOpen: true });
    expect(screen.getByText("ffmpeg path")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /Advanced/ }));
    unmount();

    renderSection({ defaultOpen: true });
    expect(screen.queryByText("ffmpeg path")).not.toBeInTheDocument();
  });

  it("opens automatically while forceOpen is set", () => {
    const { rerender } = render(
      <AdvancedSection id="downloads" count={2} forceOpen={false}>
        <div>bandwidth</div>
      </AdvancedSection>,
    );
    expect(screen.queryByText("bandwidth")).not.toBeInTheDocument();

    rerender(
      <AdvancedSection id="downloads" count={2} forceOpen>
        <div>bandwidth</div>
      </AdvancedSection>,
    );
    expect(screen.getByText("bandwidth")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Advanced/ })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
  });

  it("opens on the first render when forceOpen is already set", () => {
    render(
      <AdvancedSection id="downloads" forceOpen>
        <div>bandwidth</div>
      </AdvancedSection>,
    );

    expect(screen.getByText("bandwidth")).toBeInTheDocument();
  });

  it("re-expands when a new reason to force it open arrives after a manual collapse", async () => {
    const user = userEvent.setup();
    const { rerender } = render(
      <AdvancedSection id="downloads" forceOpen>
        <div>bandwidth</div>
      </AdvancedSection>,
    );

    await user.click(screen.getByRole("button", { name: /Advanced/ }));
    expect(screen.queryByText("bandwidth")).not.toBeInTheDocument();

    // The reason clears (the field was saved), then a different field inside
    // goes dirty. The stale manual collapse must not keep it hidden.
    rerender(
      <AdvancedSection id="downloads" forceOpen={false}>
        <div>bandwidth</div>
      </AdvancedSection>,
    );
    expect(screen.queryByText("bandwidth")).not.toBeInTheDocument();

    rerender(
      <AdvancedSection id="downloads" forceOpen>
        <div>bandwidth</div>
      </AdvancedSection>,
    );
    expect(screen.getByText("bandwidth")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Advanced/ })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
  });

  it("lets an auto-expanded section be collapsed again", async () => {
    const user = userEvent.setup();
    render(
      <AdvancedSection id="downloads" forceOpen>
        <div>bandwidth</div>
      </AdvancedSection>,
    );

    await user.click(screen.getByRole("button", { name: /Advanced/ }));
    expect(screen.queryByText("bandwidth")).not.toBeInTheDocument();
  });
});
