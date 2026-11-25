import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import PlaybackSettings from "./PlaybackSettings";

window.HTMLElement.prototype.hasPointerCapture ??= () => false;
window.HTMLElement.prototype.setPointerCapture ??= () => {};
window.HTMLElement.prototype.releasePointerCapture ??= () => {};
window.HTMLElement.prototype.scrollIntoView ??= () => {};

const A = "/dev/dri/renderD128";
const B = "/dev/dri/renderD129";
const C = "/dev/dri/renderD130";

const { settings, save } = vi.hoisted(() => ({
  settings: { current: {} as Record<string, string> },
  save: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerSettings: () => ({ data: settings.current, isLoading: false }),
  useAdminSensitiveStatus: () => ({ data: { configured: [], managed_by_env: [] }, isError: false }),
  useUpdateServerSettings: () => ({ mutateAsync: save, isPending: false }),
}));
vi.mock("@/hooks/queries/admin/system", () => ({
  useHWAccelDetection: () => ({
    data: {
      resolved: "vaapi",
      render_device_details: [
        { path: A, description: "GPU A" },
        { path: B, description: "GPU B" },
        { path: C, description: "GPU C" },
      ],
    },
    isLoading: false,
  }),
}));
vi.mock("@/hooks/queries/admin/nodes", () => ({
  useAdminNodes: () => ({ data: [], isSuccess: true }),
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useLibraryCapabilities: () => ({ data: { trickplay: false } }),
}));
vi.mock("@/hooks/queries/admin/markers", () => ({
  useAdminMarkerCapabilities: () => ({ data: undefined }),
}));
vi.mock("@/hooks/queries/admin/trickplay", () => ({
  useAdminTrickplayLibraries: () => ({ data: [], isSuccess: true }),
}));
vi.mock("@/hooks/useRestartKeys", () => ({ useRestartKeys: () => ({ has: () => false }) }));
vi.mock("./MarkerTasksCard", () => ({ MarkerTasksCard: () => null }));

const page = (
  <MemoryRouter>
    <PlaybackSettings />
  </MemoryRouter>
);

async function openDevices() {
  const transcoding = within(screen.getByRole("group", { name: "Transcoding" }));
  await userEvent.click(transcoding.getByRole("button", { name: /Advanced/ }));
}

async function switchToNvenc() {
  await userEvent.click(screen.getByRole("combobox", { name: "Hardware acceleration" }));
  await userEvent.click(screen.getByRole("option", { name: "NVIDIA NVENC" }));
}

describe("hardware device picker kept for an unsaved change", () => {
  beforeEach(() => {
    // Disclosures remember being opened; start every case collapsed.
    localStorage.clear();
    settings.current = { "playback.hw_accel": "vaapi", "playback.hw_device": A };
  });

  it("under NVENC only takes the staged device back off", async () => {
    render(page);
    await openDevices();
    await userEvent.click(screen.getByRole("switch", { name: "GPU B" }));
    await switchToNvenc();

    // A render path NVENC can't use can't be added, and the staged one can
    // go back off.
    expect(screen.getByRole("switch", { name: "GPU C" })).toBeDisabled();
    await userEvent.click(screen.getByRole("switch", { name: "GPU B" }));

    expect(screen.queryByRole("switch", { name: "GPU B" })).not.toBeInTheDocument();
    expect(screen.getByText("1 unsaved change")).toBeInTheDocument();
  });

  it("can restore a saved list that differs from the staged one only in order", async () => {
    const { rerender } = render(page);
    await openDevices();
    await userEvent.click(screen.getByRole("switch", { name: "GPU B" }));
    // Someone else saves the same devices in the other order.
    settings.current = { "playback.hw_accel": "vaapi", "playback.hw_device": `${B},${A}` };
    rerender(page);
    await switchToNvenc();

    await userEvent.click(screen.getByRole("switch", { name: "GPU A" }));
    await userEvent.click(screen.getByRole("switch", { name: "GPU A" }));

    expect(screen.queryByRole("switch", { name: "GPU A" })).not.toBeInTheDocument();
    expect(screen.getByText("1 unsaved change")).toBeInTheDocument();
  });
});

describe("marker settings kept for an unsaved change", () => {
  beforeEach(() => {
    localStorage.clear();
    settings.current = {
      "markers.mode": "local",
      "markers.online_storage": "stored",
      "markers.lazy_playback": "false",
      "markers.detection_workers": "1",
    };
  });

  async function pick(label: string, option: string) {
    await userEvent.click(screen.getByRole("combobox", { name: label }));
    await userEvent.click(screen.getByRole("option", { name: option }));
  }

  it("keeps on-demand storage in view after online markers are turned off", async () => {
    render(page);
    await pick("Marker source", "Online preferred + server detection");
    await pick("Save online markers", "Fetch when needed");
    await pick("Marker source", "Detect on this server");

    expect(screen.getByRole("combobox", { name: "Save online markers" })).toHaveTextContent(
      "Fetch when needed",
    );
    expect(screen.getByText("1 unsaved change")).toBeInTheDocument();

    await pick("Save online markers", "Save to library");
    expect(screen.queryByText(/unsaved change/)).not.toBeInTheDocument();
  });

  it("leaves Find markers on playback in view and unchanged when on-demand storage is chosen with server detection", async () => {
    settings.current["markers.mode"] = "both";
    save.mockReset();
    save.mockResolvedValue({ values: {} });
    render(page);
    await pick("Save online markers", "Fetch when needed");

    // Lazy playback turns on local detection at playback under this source,
    // so its row stays and on-demand storage doesn't switch it on.
    expect(screen.getByRole("switch", { name: "Find markers on playback" })).not.toBeChecked();
    expect(screen.getByText("1 unsaved change")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(save).toHaveBeenCalledWith({ "markers.online_storage": "on_demand" });
  });

  it("keeps an edited detection worker count in view under online-only markers", async () => {
    render(page);
    const workers = screen.getByRole("spinbutton", { name: "Detection workers" });
    await userEvent.clear(workers);
    await userEvent.type(workers, "3");
    await pick("Marker source", "Online providers only");

    expect(screen.getByRole("spinbutton", { name: "Detection workers" })).toHaveValue(3);
  });
});

describe("paused playback timeout", () => {
  beforeEach(() => {
    localStorage.clear();
    save.mockReset();
    settings.current = { "playback.paused_session_timeout_minutes": "20" };
  });

  // Zero is the documented way to turn the timeout off, so it must save.
  it("shows the stored minutes and saves zero", async () => {
    render(page);
    const group = within(screen.getByRole("group", { name: "Paused playback" }));
    const field = group.getByRole("spinbutton", { name: "End paused playback after" });
    expect(field).toHaveValue(20);

    await userEvent.clear(field);
    await userEvent.type(field, "0");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(save).toHaveBeenCalledWith(
      expect.objectContaining({ "playback.paused_session_timeout_minutes": "0" }),
    );
  });
});
