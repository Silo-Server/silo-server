import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PlayerSettingsMenu } from "./PlayerSettingsMenu";
import { makePrefs } from "./playerTestUtils";

async function openMenu() {
  await userEvent.click(screen.getByRole("button", { name: "Player settings" }));
  return screen.getByRole("dialog", { name: "Player settings" });
}

describe("PlayerSettingsMenu", () => {
  it("shows the current intervals and writes through the parent's setters", async () => {
    const setSkipBack = vi.fn();
    const setSkipForward = vi.fn();
    render(
      <PlayerSettingsMenu
        prefs={makePrefs({ skipBack: 15, skipForward: 60, setSkipBack, setSkipForward })}
      />,
    );
    await openMenu();

    const back = within(screen.getByRole("group", { name: "Skip back" }));
    const forward = within(screen.getByRole("group", { name: "Skip forward" }));
    expect(back.getByRole("button", { name: "15s", pressed: true })).toBeInTheDocument();
    expect(forward.getByRole("button", { name: "60s", pressed: true })).toBeInTheDocument();
    expect(screen.getByText("Saved to your profile for every Silo app")).toBeInTheDocument();

    await userEvent.click(back.getByRole("button", { name: "45s", pressed: false }));
    expect(setSkipBack).toHaveBeenCalledWith(45);
    expect(setSkipForward).not.toHaveBeenCalled();
  });

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

  it("names the browser as the store on a server without shared intervals", async () => {
    render(<PlayerSettingsMenu prefs={makePrefs({ hasSharedSkipIntervals: false })} />);
    await openMenu();

    expect(screen.getByText("Saved on this browser only")).toBeInTheDocument();
    for (const button of screen.getAllByRole("button", { name: /^\d+s$/ })) {
      expect(button).toBeEnabled();
    }
  });

  it("says when the profile's intervals could not be read", async () => {
    render(<PlayerSettingsMenu prefs={makePrefs({ sharedSkipIntervalsError: true })} />);
    await openMenu();

    expect(
      screen.getByText("Couldn't load your profile's intervals; showing defaults"),
    ).toBeInTheDocument();
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
