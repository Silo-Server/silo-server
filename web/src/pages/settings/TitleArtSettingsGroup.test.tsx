import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { TitleArtSettingsGroup } from "./TitleArtSettingsGroup";

const mocks = vi.hoisted(() => ({
  setting: { value: true, source: "default" as string },
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
    isPending: false,
    isError: false,
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
    mocks.setting = { value: true, source: "default" };
    mocks.supported = true;
    mocks.profileActive = true;
    mocks.set.mockReset().mockResolvedValue(undefined);
    mocks.clear.mockReset().mockResolvedValue(undefined);
  });

  it("stays hidden on a server that predates the setting", () => {
    mocks.supported = false;
    const { container } = render(<TitleArtSettingsGroup />);

    expect(container).toBeEmptyDOMElement();
  });

  it("saves this browser's own choice while not applied to all devices", async () => {
    render(<TitleArtSettingsGroup />);

    expect(screen.getByRole("switch", { name: "Apply to all devices" })).not.toBeChecked();
    expect(screen.getByText(/Only affects this browser/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("switch", { name: "Show title art" }));

    expect(mocks.set).toHaveBeenCalledWith({
      key: KEY,
      value: false,
      identity: { scope: "profile_device" },
    });
  });

  it("explains and edits the profile-wide choice", async () => {
    mocks.setting = { value: false, source: "profile" };
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
    mocks.setting = { value: false, source: "profile_device" };
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
    mocks.setting = { value: false, source: "profile" };
    mocks.set.mockImplementation(async () => {
      mocks.profileActive = false;
    });
    render(<TitleArtSettingsGroup />);

    await userEvent.click(screen.getByRole("switch", { name: "Apply to all devices" }));

    await waitFor(() => expect(mocks.set).toHaveBeenCalledTimes(1));
    expect(mocks.clear).not.toHaveBeenCalled();
  });

  it("pins this browser before handing control back to each device", async () => {
    mocks.setting = { value: false, source: "profile" };
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
