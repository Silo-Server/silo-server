import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { PlaybackStep } from "./PlaybackStep";

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver ??= ResizeObserverStub as unknown as typeof ResizeObserver;
if (!window.HTMLElement.prototype.hasPointerCapture) {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
}
if (!window.HTMLElement.prototype.scrollIntoView) {
  window.HTMLElement.prototype.scrollIntoView = () => {};
}

const useSettingsFormMock = vi.fn();
const useWizardContextMock = vi.fn();
const useHWAccelDetectionMock = vi.fn();

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("../WizardContext", () => ({
  useWizardContext: (...args: unknown[]) => useWizardContextMock(...args),
}));

vi.mock("@/hooks/queries/admin/system", () => ({
  useHWAccelDetection: (...args: unknown[]) => useHWAccelDetectionMock(...args),
}));

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

function mockStep(values: Record<string, string> = {}, dirtyCount = 0) {
  const formValues: Record<string, string> = { "playback.hw_accel": "auto", ...values };
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
    loaded: true,
    loadError: false,
    getValue: (key: string) => formValues[key] ?? "",
    setValue,
    isDirty: () => false,
    dirtyCount,
    dirtyKeys: [],
    save,
    discard: vi.fn(),
    isSaving: false,
  });
  return { markDone, save, setValue, setSummary };
}

describe("PlaybackStep", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useHWAccelDetectionMock.mockReturnValue({ data: undefined, isLoading: false, isError: false });
  });

  it("names the detected GPU under the hardware acceleration field", () => {
    useHWAccelDetectionMock.mockReturnValue({
      data: {
        resolved: "nvenc",
        render_devices: ["/dev/dri/renderD128"],
        source: "local",
        tone_map_capabilities: [
          { mode: "hardware", backend: "cuda", filter: "tonemap_cuda", source_kinds: ["hdr10"] },
        ],
      },
      isLoading: false,
      isError: false,
    });
    const { setSummary } = mockStep();
    render(<PlaybackStep />);

    expect(screen.getByText("Detected NVIDIA NVENC on /dev/dri/renderD128")).toBeInTheDocument();
    expect(screen.getByText("Hardware tone mapper available")).toBeInTheDocument();
    expect(setSummary).toHaveBeenCalledWith("playback", "NVIDIA NVENC");
  });

  it("does not probe hardware when acceleration is set to software", () => {
    mockStep({ "playback.hw_accel": "none" });
    render(<PlaybackStep />);

    expect(useHWAccelDetectionMock).toHaveBeenCalledWith(false);
    expect(screen.queryByText(/Detected/)).not.toBeInTheDocument();
  });

  it("exposes both tone-mapping toggles and stages the chosen values", async () => {
    const { setValue } = mockStep();
    render(<PlaybackStep />);

    await userEvent.click(screen.getByRole("switch", { name: "On the GPU" }));
    await userEvent.click(screen.getByRole("switch", { name: "On the CPU" }));

    expect(setValue).toHaveBeenCalledWith("playback.transcode_hardware_tone_map_enabled", "true");
    expect(setValue).toHaveBeenCalledWith("playback.transcode_software_tone_map_enabled", "true");
  });

  it("continues without saving when nothing changed", async () => {
    const { markDone, save } = mockStep();
    render(<PlaybackStep />);

    await userEvent.click(screen.getByRole("button", { name: "Continue" }));

    expect(save).not.toHaveBeenCalled();
    expect(markDone).toHaveBeenCalledWith("playback");
  });

  it("saves then marks the step done when something changed", async () => {
    const { markDone, save } = mockStep({}, 1);
    render(<PlaybackStep />);

    await userEvent.click(screen.getByRole("button", { name: "Continue" }));

    expect(save).toHaveBeenCalledTimes(1);
    expect(markDone).toHaveBeenCalledWith("playback");
  });
});
