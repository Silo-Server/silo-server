import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PlayerSettingsMenu } from "./PlayerSettingsMenu";
import { makePrefs } from "./playerTestUtils";

async function openMenu() {
  await userEvent.click(screen.getByRole("button", { name: "Player settings" }));
  return screen.getByRole("dialog", { name: "Player settings" });
}

describe("PlayerSettingsMenu", () => {
  it("disables the interval buttons, not smart rewind, while a profile write is pending", async () => {
    const setSmartRewind = vi.fn();
    render(
      <PlayerSettingsMenu prefs={makePrefs({ isSavingSkipIntervals: true, setSmartRewind })} />,
    );
    await openMenu();

    for (const button of screen.getAllByRole("button", { name: /^\d+s$/ })) {
      expect(button).toBeDisabled();
    }
    const smartRewind = screen.getByRole("switch", { name: /Smart rewind/ });
    expect(smartRewind).toBeEnabled();
    await userEvent.click(smartRewind);
    expect(setSmartRewind).toHaveBeenCalledWith(false);
  });

  it("keeps skip interval controls enabled on a server without shared intervals", async () => {
    render(<PlayerSettingsMenu prefs={makePrefs({ hasSharedSkipIntervals: false })} />);
    await openMenu();

    for (const button of screen.getAllByRole("button", { name: /^\d+s$/ })) {
      expect(button).toBeEnabled();
    }
  });

  it("keeps the interval controls read-only while server support is unknown", async () => {
    const setSkipBack = vi.fn();
    render(
      <PlayerSettingsMenu
        prefs={makePrefs({
          canEditSkipIntervals: false,
          hasSharedSkipIntervals: false,
          setSkipBack,
        })}
      />,
    );
    await openMenu();

    expect(screen.getByRole("group", { name: "Skip back" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    for (const button of screen.getAllByRole("button", { name: /^\d+s$/ })) {
      expect(button).toBeDisabled();
    }
    expect(screen.getByText("Checking what this server supports…")).toBeInTheDocument();
    expect(setSkipBack).not.toHaveBeenCalled();
  });
});
