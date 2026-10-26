import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { TitleArtSettingsGroup } from "./TitleArtSettingsGroup";

const mocks = vi.hoisted(() => ({
  setting: { value: true, source: "default" as string, isPending: false, isError: false },
  supported: true,
  set: vi.fn(),
  clear: vi.fn(),
  missing: new Error("missing"),
  profileActive: true,
}));

vi.mock("@/api/client", () => ({
  captureProfileRequestContext: () => ({ profileId: "p1" }),
  isCapturedProfileAuthorityActive: () => mocks.profileActive,
}));

vi.mock("@/hooks/queries/settingValues", () => ({
  useSettingValue: () => ({
    value: mocks.setting.value,
    source: mocks.setting.source,
    isPending: mocks.setting.isPending,
    isError: mocks.setting.isError,
  }),
  useSetSettingValue: () => ({ mutateAsync: mocks.set, isPending: false }),
  useClearSettingValue: () => ({ mutateAsync: mocks.clear, isPending: false }),
  useSettingsCapabilities: () => ({ data: {} }),
  settingsCapabilitiesSupportKey: () => mocks.supported,
  isSettingValueMissing: (error: unknown) => error === mocks.missing,
}));

vi.mock("sonner", () => ({ toast: { error: vi.fn() } }));

const KEY = "ui.title_art";

describe("TitleArtSettingsGroup", () => {
  beforeEach(() => {
    mocks.setting = { value: true, source: "default", isPending: false, isError: false };
    mocks.supported = true;
    mocks.profileActive = true;
    mocks.set.mockReset().mockResolvedValue(undefined);
    mocks.clear.mockReset().mockResolvedValue(undefined);
  });

  it("saves this browser's own choice while not applied to all devices", async () => {
    render(<TitleArtSettingsGroup />);

    expect(screen.getByRole("switch", { name: "Apply to all devices" })).not.toBeChecked();
    expect(screen.getByText(/Using the server default \(on\)/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("switch", { name: "Show title art" }));

    expect(mocks.set).toHaveBeenCalledWith({
      key: KEY,
      value: false,
      identity: { scope: "profile_device" },
    });
  });

  it("explains and edits the profile-wide choice", async () => {
    mocks.setting = { value: false, source: "profile", isPending: false, isError: false };
    render(<TitleArtSettingsGroup />);

    expect(screen.getByRole("switch", { name: "Apply to all devices" })).toBeChecked();
    expect(screen.getByText(/Title art is off on every device/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("switch", { name: "Show title art" }));

    expect(mocks.set).toHaveBeenCalledWith({
      key: KEY,
      value: true,
      identity: { scope: "profile" },
    });
  });

  it("applies the current choice to every device", async () => {
    mocks.setting = { value: false, source: "profile_device", isPending: false, isError: false };
    render(<TitleArtSettingsGroup />);

    await userEvent.click(screen.getByRole("switch", { name: "Apply to all devices" }));

    expect(mocks.set).toHaveBeenCalledWith({
      key: KEY,
      value: false,
      identity: { scope: "profile" },
    });
    expect(mocks.clear).not.toHaveBeenCalled();
  });

  it("leaves another profile's choice alone after a profile switch mid-change", async () => {
    mocks.setting = { value: false, source: "profile", isPending: false, isError: false };
    mocks.set.mockImplementation(async () => {
      mocks.profileActive = false;
    });
    render(<TitleArtSettingsGroup />);

    await userEvent.click(screen.getByRole("switch", { name: "Apply to all devices" }));

    await waitFor(() => expect(mocks.set).toHaveBeenCalledTimes(1));
    expect(mocks.clear).not.toHaveBeenCalled();
  });

  it.each(["profile_device", "profile"])(
    "returns a %s choice to the server default without saving it",
    async (source) => {
      mocks.setting = { value: true, source, isPending: false, isError: false };
      mocks.clear.mockImplementation(async ({ identity }: { identity: { scope: string } }) => {
        if (identity.scope !== source) throw mocks.missing;
      });
      render(<TitleArtSettingsGroup />);

      expect(screen.queryByText(/Using the server default/)).not.toBeInTheDocument();
      if (source === "profile_device") {
        expect(screen.getByText(/Only affects this browser/)).toBeInTheDocument();
      }
      await userEvent.click(screen.getByRole("button", { name: "Use server default" }));

      // A device value means no profile value exists, so only the profile
      // source also clears the profile-wide choice.
      const clears = source === "profile" ? 2 : 1;
      await waitFor(() => expect(mocks.clear).toHaveBeenCalledTimes(clears));
      expect(mocks.clear).toHaveBeenNthCalledWith(1, {
        key: KEY,
        identity: { scope: "profile_device" },
      });
      if (source === "profile") {
        expect(mocks.clear).toHaveBeenNthCalledWith(2, {
          key: KEY,
          identity: { scope: "profile" },
        });
      }
      expect(mocks.set).not.toHaveBeenCalled();
    },
  );

  it.each([["failed", { isPending: false, isError: true }]])(
    "claims neither the server default nor an own choice while %s",
    (_, state) => {
      // useSettingValue reports the contract default with source "default" here,
      // which says nothing about what this server would answer.
      mocks.setting = { value: true, source: "default", ...state };
      render(<TitleArtSettingsGroup />);

      expect(screen.queryByText(/Using the server default/)).not.toBeInTheDocument();
      expect(screen.queryByText(/Only affects this browser/)).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Use server default" })).not.toBeInTheDocument();
    },
  );

  it("pins this browser before handing control back to each device", async () => {
    mocks.setting = { value: false, source: "profile", isPending: false, isError: false };
    mocks.clear.mockRejectedValue(mocks.missing);
    render(<TitleArtSettingsGroup />);

    await userEvent.click(screen.getByRole("switch", { name: "Apply to all devices" }));

    await waitFor(() =>
      expect(mocks.clear).toHaveBeenCalledWith({ key: KEY, identity: { scope: "profile" } }),
    );
    expect(mocks.set).toHaveBeenCalledWith({
      key: KEY,
      value: false,
      identity: { scope: "profile_device" },
    });
    const [writeOrder] = mocks.set.mock.invocationCallOrder;
    const [clearOrder] = mocks.clear.mock.invocationCallOrder;
    expect(writeOrder).toBeLessThan(clearOrder ?? 0);
  });
});
