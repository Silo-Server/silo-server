import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import PlaybackSettings from "./PlaybackSettings";

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
const useHWAccelDetectionMock = vi.fn();
const useAdminNodesMock = vi.fn();

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => new Set<string>(["playback.ffmpeg_path"]),
}));

vi.mock("@/hooks/queries/admin/system", () => ({
  useHWAccelDetection: (...args: unknown[]) => useHWAccelDetectionMock(...args),
}));

vi.mock("@/hooks/queries/admin/nodes", () => ({
  useAdminNodes: () => useAdminNodesMock(),
}));

/** A transcode node the chapter-thumbnail extractor could reserve. */
function transcodeNode(overrides: Record<string, unknown> = {}) {
  return { id: 1, name: "node-1", type: "transcode", enabled: true, healthy: true, ...overrides };
}

function makeForm(values: Record<string, string>, dirty: string[] = []) {
  const dirtyKeys = new Set(dirty);
  return {
    isLoading: false,
    getValue: (key: string) => values[key] ?? "",
    setValue: vi.fn(),
    isDirty: (key: string) => dirtyKeys.has(key),
    dirtyCount: dirtyKeys.size,
    dirtyKeys: [...dirtyKeys],
    save: vi.fn(),
    discard: vi.fn(),
    isSaving: false,
    restartRequired: false,
  };
}

function parse(markup: string): HTMLElement {
  const container = document.createElement("div");
  container.innerHTML = markup;
  return container;
}

function labelled(container: HTMLElement, text: string): Element {
  const label = Array.from(container.querySelectorAll("label")).find(
    (candidate) => candidate.textContent === text,
  );
  const control = label?.htmlFor ? container.querySelector(`[id="${label.htmlFor}"]`) : null;
  if (!control) throw new Error(`no control rendered for label: ${text}`);
  return control;
}

/** Opens the page's advanced disclosure via its persisted state. */
function expandAdvanced() {
  localStorage.setItem("silo.admin.advanced.playback.transcoding", "true");
}

const TONE_MAP_LABEL = "Software HDR tone mapping";

beforeEach(() => {
  localStorage.clear();
  useSettingsFormMock.mockReset();
  useHWAccelDetectionMock.mockReset();
  useHWAccelDetectionMock.mockReturnValue({ data: undefined, isLoading: false });
  useAdminNodesMock.mockReset();
  useAdminNodesMock.mockReturnValue({ data: [transcodeNode()], isSuccess: true });
});

describe("PlaybackSettings layout", () => {
  it("renders every field group heading", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    const container = parse(renderToStaticMarkup(<PlaybackSettings />));
    const headings = Array.from(container.querySelectorAll("[role=group]")).map((group) => {
      const labelId = group.getAttribute("aria-labelledby");
      return labelId ? (container.querySelector(`[id="${labelId}"]`)?.textContent ?? "") : "";
    });

    expect(headings).toEqual(["Transcoding", "Node routing", "Watch behavior"]);
  });

  it("opens with the title alone: no breadcrumb, lede, or status strip", () => {
    useSettingsFormMock.mockReturnValue(
      makeForm({ "playback.hw_accel": "none", "playback.transcode_enabled": "true" }),
    );

    const container = parse(renderToStaticMarkup(<PlaybackSettings />));

    expect(container.querySelector("h1")?.textContent).toBe("Playback");
    expect(container.textContent).not.toContain("Settings ›");
    expect(container.textContent).not.toContain("Transcoding on");
    expect(container.textContent).not.toContain("Restart pending");
  });

  it("puts the percent unit beside the control instead of in the label", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.watched_threshold": "90" }));

    const container = parse(renderToStaticMarkup(<PlaybackSettings />));

    expect(labelled(container, "Mark watched at")).toHaveAttribute("value", "90");
    expect(container.textContent).not.toContain("Mark watched at (%)");
  });

  it("manages the playback key family and leaves downloads to their own page", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    renderToStaticMarkup(<PlaybackSettings />);
    const keys: string[] = useSettingsFormMock.mock.calls[0]?.[0]?.keys ?? [];

    expect(keys).toContain("playback.transcode_enabled");
    expect(keys).toContain("playback.routing.video_transcode_egress");
    expect(keys).toContain("playback.watched_threshold");
    expect(keys.some((key) => key.startsWith("download."))).toBe(false);
    // Hidden tier: still saved and readable through the API, no UI.
    expect(keys).not.toContain("playback.chapter_thumbnail_node_capacity");
  });

  it("keeps advanced settings collapsed until they are opened", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    const container = parse(renderToStaticMarkup(<PlaybackSettings />));

    expect(container.textContent).toContain("Transcoding");
    expect(container.textContent).not.toContain("FFmpeg path");
  });

  it("force-opens an advanced section holding a dirty field", () => {
    useSettingsFormMock.mockReturnValue(
      makeForm({ "playback.hw_accel": "none" }, ["playback.ffmpeg_path"]),
    );

    const container = parse(renderToStaticMarkup(<PlaybackSettings />));

    expect(container.textContent).toContain("FFmpeg path");
  });

  it("marks restart-required fields from the restart key list", () => {
    expandAdvanced();
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    const container = parse(renderToStaticMarkup(<PlaybackSettings />));
    const badges = container.querySelectorAll("[aria-label='Takes effect after a server restart']");

    expect(badges).toHaveLength(1);
  });
});

describe("PlaybackSettings node routing", () => {
  const defaultRouting = {
    "playback.routing.direct_play_egress": "prefer_proxy",
    "playback.routing.remux_execution": "prefer_transcode",
    "playback.routing.remux_egress": "prefer_proxy",
    "playback.routing.video_transcode_execution": "prefer_transcode",
    "playback.routing.video_transcode_egress": "prefer_proxy",
  };

  it("stages every primitive setting when a preset is selected", async () => {
    const form = makeForm({ "playback.hw_accel": "none", ...defaultRouting });
    useSettingsFormMock.mockReturnValue(form);

    render(<PlaybackSettings />);
    await userEvent.click(screen.getByRole("button", { name: "GPU offload" }));

    expect(form.setValue.mock.calls).toEqual([
      ["playback.routing.direct_play_egress", "prefer_api"],
      ["playback.routing.remux_execution", "prefer_api"],
      ["playback.routing.remux_egress", "prefer_api"],
      ["playback.routing.video_transcode_execution", "prefer_worker"],
      ["playback.routing.video_transcode_egress", "prefer_proxy"],
    ]);
    expect(form.save).not.toHaveBeenCalled();
  });

  it("labels the built-in routing policy as Silo Defaults", () => {
    useSettingsFormMock.mockReturnValue(
      makeForm({ "playback.hw_accel": "none", ...defaultRouting }),
    );

    render(<PlaybackSettings />);

    expect(screen.getByRole("button", { name: "Silo Defaults" })).toBeVisible();
    expect(screen.queryByRole("button", { name: "Standard cluster" })).not.toBeInTheDocument();
    expect(screen.queryByText("Custom")).not.toBeInTheDocument();
  });

  it("offers a transcode-node preference and explains what a worker is", async () => {
    useSettingsFormMock.mockReturnValue(
      makeForm({
        "playback.hw_accel": "none",
        ...defaultRouting,
      }),
    );

    render(<PlaybackSettings />);

    const remuxExecution = screen.getByRole("combobox", { name: "Remux execution" });
    expect(remuxExecution).toHaveTextContent("Prefer transcode node");
    await userEvent.click(remuxExecution);
    expect(await screen.findByRole("option", { name: "Prefer any worker" })).toBeVisible();
    expect(screen.getByRole("option", { name: "Prefer transcode node" })).toBeVisible();
    expect(screen.getByText(/A worker can be a proxy/)).toBeVisible();
    expect(screen.getByText(/Transcode node → any worker → API/)).toBeVisible();
    expect(screen.getByText(/Video transcode workers are transcode nodes/)).toBeVisible();
  });

  it("warns when hard routes lack nodes or universal client-origin support", () => {
    useAdminNodesMock.mockReturnValue({
      data: [transcodeNode({ healthy: false })],
      isSuccess: true,
    });
    useSettingsFormMock.mockReturnValue(
      makeForm({
        "playback.hw_accel": "none",
        ...defaultRouting,
        "playback.routing.direct_play_egress": "proxy_only",
        "playback.routing.remux_execution": "worker_only",
      }),
    );

    const text = parse(renderToStaticMarkup(<PlaybackSettings />)).textContent ?? "";

    expect(text).toContain("no healthy supporting node");
    expect(text).toContain("requires every native client to support authorized media origins");
  });
});

describe("PlaybackSettings CPU tone mapping", () => {
  it("includes the setting and renders it off by default", () => {
    expandAdvanced();
    useSettingsFormMock.mockReturnValue(
      makeForm({
        "playback.hw_accel": "none",
        "playback.chapter_thumbnail_hdr_policy": "best_effort",
      }),
    );

    const container = parse(renderToStaticMarkup(<PlaybackSettings />));

    expect(useSettingsFormMock.mock.calls[0]?.[0]?.keys).toContain(
      "playback.chapter_thumbnail_software_tone_map_enabled",
    );
    const toggle = labelled(container, TONE_MAP_LABEL);
    expect(toggle).toHaveAttribute("aria-checked", "false");
    expect(toggle).not.toHaveAttribute("disabled");
  });

  it("offers VideoToolbox hardware acceleration", async () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "auto" }));

    render(<PlaybackSettings />);
    await userEvent.click(screen.getByRole("combobox", { name: "Hardware acceleration" }));

    expect(screen.getByRole("option", { name: "VideoToolbox (macOS)" })).toBeInTheDocument();
  });

  it("disables the toggle while HDR chapter thumbnails are disabled", () => {
    expandAdvanced();
    useSettingsFormMock.mockReturnValue(
      makeForm({
        "playback.hw_accel": "none",
        "playback.chapter_thumbnail_hdr_policy": "disabled",
        "playback.chapter_thumbnail_software_tone_map_enabled": "true",
      }),
    );

    const container = parse(renderToStaticMarkup(<PlaybackSettings />));
    const toggle = labelled(container, TONE_MAP_LABEL);

    expect(toggle).toHaveAttribute("aria-checked", "true");
    expect(toggle).toHaveAttribute("disabled");
  });
});

describe("PlaybackSettings transcode tone mapping", () => {
  beforeEach(expandAdvanced);

  it("registers independent hardware and software settings disabled by default", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "auto" }));

    const container = parse(renderToStaticMarkup(<PlaybackSettings />));
    const keys = useSettingsFormMock.mock.calls[0]?.[0]?.keys as string[];

    expect(keys).toContain("playback.transcode_hardware_tone_map_enabled");
    expect(keys).toContain("playback.transcode_software_tone_map_enabled");
    expect(labelled(container, "Enable Hardware HDR Tone Mapping")).toHaveAttribute(
      "aria-checked",
      "false",
    );
    expect(labelled(container, "Enable Software HDR Tone Mapping")).toHaveAttribute(
      "aria-checked",
      "false",
    );
  });

  it("keeps the two policies independent", () => {
    useSettingsFormMock.mockReturnValue(
      makeForm({
        "playback.hw_accel": "qsv",
        "playback.transcode_hardware_tone_map_enabled": "true",
        "playback.transcode_software_tone_map_enabled": "false",
      }),
    );

    const container = parse(renderToStaticMarkup(<PlaybackSettings />));

    expect(labelled(container, "Enable Hardware HDR Tone Mapping")).toHaveAttribute(
      "aria-checked",
      "true",
    );
    expect(labelled(container, "Enable Software HDR Tone Mapping")).toHaveAttribute(
      "aria-checked",
      "false",
    );
  });

  it("keeps hardware tone mapping configurable for remote executors when local acceleration is off", () => {
    useSettingsFormMock.mockReturnValue(
      makeForm({
        "playback.hw_accel": "none",
        "playback.transcode_hardware_tone_map_enabled": "true",
      }),
    );

    const toggle = labelled(
      parse(renderToStaticMarkup(<PlaybackSettings />)),
      "Enable Hardware HDR Tone Mapping",
    );

    expect(toggle).toHaveAttribute("aria-checked", "true");
    expect(toggle).not.toHaveAttribute("disabled");
  });

  it("keeps hardware tone mapping configurable when one detected executor is software-only", () => {
    useHWAccelDetectionMock.mockReturnValue({
      data: {
        resolved: "none",
        nodes: [
          { node_url: "http://software-node", resolved: "none" },
          { node_url: "http://gpu-node", resolved: "qsv" },
        ],
      },
      isLoading: false,
    });
    useSettingsFormMock.mockReturnValue(
      makeForm({
        "playback.hw_accel": "auto",
        "playback.transcode_hardware_tone_map_enabled": "true",
      }),
    );

    const toggle = labelled(
      parse(renderToStaticMarkup(<PlaybackSettings />)),
      "Enable Hardware HDR Tone Mapping",
    );

    expect(toggle).toHaveAttribute("aria-checked", "true");
    expect(toggle).not.toHaveAttribute("disabled");
  });
});

describe("PlaybackSettings path defaults", () => {
  beforeEach(expandAdvanced);

  const RESET_TRANSCODE_DIR = { name: "Reset Transcode directory to default" };

  it("shows the effective default of each path field as its placeholder", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    const container = parse(renderToStaticMarkup(<PlaybackSettings />));

    expect(labelled(container, "Transcode directory")).toHaveAttribute(
      "placeholder",
      "/tmp/silo-transcode",
    );
    expect(labelled(container, "FFmpeg path")).toHaveAttribute(
      "placeholder",
      "/usr/lib/jellyfin-ffmpeg/ffmpeg",
    );
  });

  it("says in words what leaving each path blank does", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    const text = parse(renderToStaticMarkup(<PlaybackSettings />)).textContent ?? "";

    expect(text).toContain("Leave blank to use /tmp/silo-transcode.");
    expect(text).toContain(
      "Leave blank to use the FFmpeg that ships with the server, at /usr/lib/jellyfin-ffmpeg/ffmpeg.",
    );
  });

  it("offers no reset while a path field already runs the default", () => {
    useSettingsFormMock.mockReturnValue(
      makeForm({ "playback.hw_accel": "none", "playback.transcode_dir": "/tmp/silo-transcode" }),
    );
    render(<PlaybackSettings />);

    expect(screen.queryByRole("button", RESET_TRANSCODE_DIR)).not.toBeInTheDocument();
  });

  it("stages an empty value when an overridden path is reset", () => {
    const form = makeForm({
      "playback.hw_accel": "none",
      "playback.transcode_dir": "/mnt/fast/transcode",
    });
    useSettingsFormMock.mockReturnValue(form);
    render(<PlaybackSettings />);

    fireEvent.click(screen.getByRole("button", RESET_TRANSCODE_DIR));

    expect(form.setValue).toHaveBeenCalledWith("playback.transcode_dir", "");
    expect(form.save).not.toHaveBeenCalled();
  });

  it("counts the reset as one unsaved change and falls back to the placeholder", () => {
    // The staged empty string, as the form would report it on the next render.
    useSettingsFormMock.mockReturnValue(
      makeForm({ "playback.hw_accel": "none" }, ["playback.transcode_dir"]),
    );
    render(<PlaybackSettings />);

    expect(screen.getByLabelText("Transcode directory")).toHaveValue("");
    expect(screen.getByText("1 unsaved change")).toBeInTheDocument();
    expect(screen.queryByRole("button", RESET_TRANSCODE_DIR)).not.toBeInTheDocument();
  });
});

describe("PlaybackSettings chapter thumbnail execution", () => {
  beforeEach(expandAdvanced);

  it("warns when no transcode node can take an extraction", () => {
    useAdminNodesMock.mockReturnValue({
      data: [transcodeNode({ healthy: false }), transcodeNode({ id: 2, type: "streaming" })],
      isSuccess: true,
    });
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    const container = parse(renderToStaticMarkup(<PlaybackSettings />));

    expect(container.textContent).toContain("No transcode nodes are connected");
  });

  it("stays quiet while a healthy transcode node is connected", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    const container = parse(renderToStaticMarkup(<PlaybackSettings />));

    expect(container.textContent).not.toContain("No transcode nodes are connected");
  });

  it("does not warn before the node list has loaded", () => {
    useAdminNodesMock.mockReturnValue({ data: undefined, isSuccess: false });
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    const container = parse(renderToStaticMarkup(<PlaybackSettings />));

    expect(container.textContent).not.toContain("No transcode nodes are connected");
  });
});

describe("PlaybackSettings divergent node inventories", () => {
  // The device picker lives behind the advanced disclosure this page grew.
  beforeEach(expandAdvanced);

  it("points at the per-node overrides on the Nodes page", () => {
    useHWAccelDetectionMock.mockReturnValue({
      data: {
        resolved: "qsv",
        render_device_details: [{ path: "/dev/dri/renderD128", description: "Intel GPU" }],
        nodes: [
          { node_url: "http://node-a", render_devices: ["/dev/dri/renderD128"] },
          { node_url: "http://node-b", render_devices: ["/dev/dri/renderD129"] },
        ],
      },
      isLoading: false,
    });
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "qsv" }));

    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <PlaybackSettings />
      </MemoryRouter>,
    );

    expect(markup).toContain("Nodes report different devices");
    expect(markup).toContain("set per-node overrides on the");
    expect(markup).toContain('href="/admin/nodes"');
  });

  it("stays quiet while every node reports the same devices", () => {
    useHWAccelDetectionMock.mockReturnValue({
      data: {
        resolved: "qsv",
        render_device_details: [{ path: "/dev/dri/renderD128", description: "Intel GPU" }],
        nodes: [
          { node_url: "http://node-a", render_devices: ["/dev/dri/renderD128"] },
          { node_url: "http://node-b", render_devices: ["/dev/dri/renderD128"] },
        ],
      },
      isLoading: false,
    });
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "qsv" }));

    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <PlaybackSettings />
      </MemoryRouter>,
    );

    expect(markup).not.toContain("set per-node overrides on the");
    expect(markup).not.toContain('href="/admin/nodes"');
  });
});
