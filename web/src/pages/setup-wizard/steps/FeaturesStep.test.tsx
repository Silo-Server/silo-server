import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { FeaturesStep } from "./FeaturesStep";

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver ??= ResizeObserverStub as unknown as typeof ResizeObserver;

const useSettingsFormMock = vi.fn();
const useWizardContextMock = vi.fn();

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("../WizardContext", () => ({
  useWizardContext: (...args: unknown[]) => useWizardContextMock(...args),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useCheckAdminSettingsConnection: () => ({ isPending: false, mutateAsync: vi.fn() }),
}));
vi.mock("@/hooks/queries/admin/system", () => ({}));

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const defaultValues: Record<string, string> = {
  "markers.mode": "both",
  "markers.lazy_playback": "true",
  "markers.online_storage": "stored",
  "notifications.apple_push_delivery_enabled": "true",
  "notifications.android_push_delivery_enabled": "true",
  "download.enabled": "false",
  "recommendations.enabled": "false",
};

function mockStep(values: Record<string, string> = {}, dirtyCount = 0, loadState = {}) {
  const formValues = { ...defaultValues, ...values };
  const markDone = vi.fn();
  const setSummary = vi.fn();
  const save = vi.fn().mockResolvedValue(undefined);
  const setValue = vi.fn((key: string, value: string) => {
    formValues[key] = value;
  });
  useWizardContextMock.mockReturnValue({ markDone, setSummary });
  useSettingsFormMock.mockReturnValue({
    isLoading: false,
    isPending: false,
    loadError: false,
    loaded: true,
    ...loadState,
    getValue: (key: string) => formValues[key] ?? "",
    setValue,
    isDirty: () => false,
    dirtyCount,
    dirtyKeys: [],
    save,
    discard: vi.fn(),
    isSaving: false,
    sensitiveConfigured: [],
    buildConnectionCheckRequest: (keys: string[]) => ({
      values: Object.fromEntries(keys.map((key) => [key, formValues[key] ?? ""])),
      dirty_keys: [],
    }),
  });
  return { markDone, save, setValue, setSummary };
}

describe("FeaturesStep", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("shows online markers and local fallback enabled for the server defaults", () => {
    mockStep();
    render(<FeaturesStep />);

    expect(screen.getByRole("switch", { name: "Skip markers from TheIntroDB" })).toBeChecked();
    expect(screen.getByRole("switch", { name: "Detect markers on this server" })).toBeChecked();
    expect(
      screen.getByText(/Online markers are preferred over local detection/),
    ).toBeInTheDocument();
  });

  it.each([
    ["both", "online", undefined],
    ["online", "both", "true"],
    ["local", "off", "false"],
    ["off", "local", "true"],
  ])(
    "changes local detection from %s to %s without changing online lookup",
    async (mode, nextMode, lazy) => {
      const { setValue } = mockStep({ "markers.mode": mode, "markers.lazy_playback": "false" });
      render(<FeaturesStep />);

      await userEvent.click(screen.getByRole("switch", { name: "Detect markers on this server" }));

      expect(setValue).toHaveBeenCalledWith("markers.mode", nextMode);
      if (lazy === undefined) {
        expect(setValue).not.toHaveBeenCalledWith("markers.lazy_playback", expect.anything());
      } else {
        expect(setValue).toHaveBeenCalledWith("markers.lazy_playback", lazy);
      }
    },
  );

  it.each([
    ["both", "local", undefined],
    ["local", "both", "true"],
    ["online", "off", "false"],
    ["off", "online", "true"],
  ])(
    "changes online lookup from %s to %s without changing local detection",
    async (mode, nextMode, lazy) => {
      const { setValue } = mockStep({ "markers.mode": mode, "markers.lazy_playback": "false" });
      render(<FeaturesStep />);

      await userEvent.click(screen.getByRole("switch", { name: "Skip markers from TheIntroDB" }));

      expect(setValue).toHaveBeenCalledWith("markers.mode", nextMode);
      if (lazy === undefined) {
        expect(setValue).not.toHaveBeenCalledWith("markers.lazy_playback", expect.anything());
      } else {
        expect(setValue).toHaveBeenCalledWith("markers.lazy_playback", lazy);
      }
    },
  );

  it("includes markers in the step summary when only local detection is enabled", () => {
    const { setSummary } = mockStep({
      "markers.mode": "local",
      "notifications.apple_push_delivery_enabled": "false",
      "notifications.android_push_delivery_enabled": "false",
    });
    render(<FeaturesStep />);

    expect(setSummary).toHaveBeenCalledWith("features", "Skip markers");
  });

  it("shows push on by default and reveals the relay disclosure on request", async () => {
    mockStep();
    render(<FeaturesStep />);

    expect(screen.getByRole("switch", { name: "Mobile push notifications" })).toBeChecked();
    expect(screen.queryByText("Privacy disclosure")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "What does the relay see?" }));

    expect(screen.getByText("Privacy disclosure")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "fully open source" })).toHaveAttribute(
      "href",
      expect.stringContaining("github.com"),
    );
  });

  it("writes both platform toggles when the admin turns push off", async () => {
    const { setValue } = mockStep();
    render(<FeaturesStep />);

    await userEvent.click(screen.getByRole("switch", { name: "Mobile push notifications" }));

    expect(setValue).toHaveBeenCalledWith("notifications.apple_push_delivery_enabled", "false");
    expect(setValue).toHaveBeenCalledWith("notifications.android_push_delivery_enabled", "false");
  });

  it("reveals bandwidth limits only while downloads are on", () => {
    mockStep({ "download.enabled": "true" });
    render(<FeaturesStep />);

    expect(screen.getByLabelText("Total download bandwidth")).toBeInTheDocument();
  });

  it.each(["stored", "on_demand"])("explains scheduled sync for %s storage", (storage) => {
    mockStep({ "markers.mode": "online", "markers.online_storage": storage });
    render(<FeaturesStep />);

    if (storage === "stored") {
      expect(screen.getByText(/daily at 03:00 \(server time\) by default/)).toBeInTheDocument();
      expect(screen.queryByText(/Scheduled online sync is disabled/)).not.toBeInTheDocument();
    } else {
      expect(screen.getByText(/Scheduled online sync is disabled/)).toBeInTheDocument();
      expect(screen.queryByText(/daily at 03:00/)).not.toBeInTheDocument();
    }
  });

  it("offers provider presets and a connection check once recommendations are on", () => {
    mockStep({ "recommendations.enabled": "true" });
    render(<FeaturesStep />);

    expect(screen.getByRole("radio", { name: /Gemini/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Check connection" })).toBeInTheDocument();
  });

  it("blocks completion when settings failed to load", () => {
    const { markDone } = mockStep({}, 0, { loadError: true, loaded: false, isPending: false });
    render(<FeaturesStep />);

    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Continue" })).not.toBeInTheDocument();
    expect(markDone).not.toHaveBeenCalled();
  });

  it("continues without saving when nothing changed", async () => {
    const { markDone, save } = mockStep();
    render(<FeaturesStep />);

    await userEvent.click(screen.getByRole("button", { name: "Continue" }));

    expect(save).not.toHaveBeenCalled();
    expect(markDone).toHaveBeenCalledWith("features");
  });

  it("saves and marks the step done when something changed", async () => {
    const { markDone, save } = mockStep({ "download.enabled": "true" }, 1);
    render(<FeaturesStep />);

    await userEvent.click(screen.getByRole("button", { name: "Continue" }));

    expect(save).toHaveBeenCalledTimes(1);
    expect(markDone).toHaveBeenCalledWith("features");
  });
});
