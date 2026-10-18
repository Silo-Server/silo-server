import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";

import { getProfileLaunchMode } from "@/lib/profileLaunch";
import { storage } from "@/utils/storage";
import { ProfileLaunchSettingsGroup } from "./ProfileLaunchSettingsGroup";

describe("ProfileLaunchSettingsGroup", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("defaults to remembering the last profile", () => {
    render(<ProfileLaunchSettingsGroup />);

    expect(screen.getByRole("radiogroup", { name: "Profile at launch" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Remember last profile" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    expect(screen.getByText(/Applies only to this browser/)).toBeInTheDocument();
  });

  it("saves the choice in this browser", async () => {
    const user = userEvent.setup();
    const view = render(<ProfileLaunchSettingsGroup />);

    await user.click(screen.getByRole("radio", { name: "Ask who's watching" }));

    expect(storage.get(storage.KEYS.PROFILE_LAUNCH)).toBe("ask");
    expect(getProfileLaunchMode()).toBe("ask");
    expect(screen.getByRole("radio", { name: "Ask who's watching" })).toHaveAttribute(
      "aria-checked",
      "true",
    );

    view.unmount();
    render(<ProfileLaunchSettingsGroup />);
    expect(screen.getByRole("radio", { name: "Ask who's watching" })).toHaveAttribute(
      "aria-checked",
      "true",
    );

    await user.click(screen.getByRole("radio", { name: "Remember last profile" }));
    expect(getProfileLaunchMode()).toBe("remember");
  });
});
