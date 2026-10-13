import { fireEvent, render, screen, within } from "@testing-library/react";
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
const useAdminTrickplayLibrariesMock = vi.fn();
const useLibraryCapabilitiesMock = vi.fn();

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

vi.mock("@/hooks/queries/admin/libraries", () => ({
  useLibraryCapabilities: () => useLibraryCapabilitiesMock(),
}));

vi.mock("@/hooks/queries/admin/trickplay", () => ({
  useAdminTrickplayLibraries: () => useAdminTrickplayLibrariesMock(),
}));

// The Preview images group links to Libraries, so the page renders in a router.
function Page() {
  return (
    <MemoryRouter>
      <PlaybackSettings />
    </MemoryRouter>
  );
}

/** A transcode node the chapter-thumbnail extractor could reserve. */
function transcodeNode(overrides: Record<string, unknown> = {}) {
  return { id: 1, name: "node-1", type: "transcode", enabled: true, healthy: true, ...overrides };
}

function makeForm(
  values: Record<string, string>,
  dirty: string[] = [],
  persisted: Record<string, string> = {},
) {
  const dirtyKeys = new Set(dirty);
  return {
    isLoading: false,
    getValue: (key: string) => values[key] ?? "",
    getPersistedValue: (key: string) => persisted[key] ?? values[key] ?? "",
    setValue: vi.fn(),
    isDirty: (key: string) => dirtyKeys.has(key),
    dirtyCount: dirtyKeys.size,
    dirtyKeys: [...dirtyKeys],
    save: vi.fn().mockResolvedValue(undefined),
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

/** The static-markup group whose heading reads `name`. */
function groupNamed(container: HTMLElement, name: string): HTMLElement {
  const group = Array.from(container.querySelectorAll<HTMLElement>("[role=group]")).find(
    (candidate) => candidate.querySelector("h3")?.textContent === name,
  );
  if (!group) throw new Error(`no group rendered with heading: ${name}`);
  return group;
}

/** Opens the Transcoding group's advanced disclosure via its persisted state. */
function expandAdvanced() {
  localStorage.setItem("silo.admin.advanced.playback.transcoding", "true");
}

/** Opens the Preview images group's disclosure via its persisted state. */
function expandPreviewImages() {
  localStorage.setItem("silo.admin.advanced.playback.preview_images", "true");
}

const TONE_MAP_LABEL = "Software HDR tone mapping";

beforeEach(() => {
  localStorage.clear();
  useLibraryCapabilitiesMock.mockReturnValue({ data: { trickplay: true } });
  useSettingsFormMock.mockReset();
  useHWAccelDetectionMock.mockReset();
  useHWAccelDetectionMock.mockReturnValue({ data: undefined, isLoading: false });
  useAdminNodesMock.mockReset();
  useAdminNodesMock.mockReturnValue({ data: [transcodeNode()], isSuccess: true });
  useAdminTrickplayLibrariesMock.mockReset();
  useAdminTrickplayLibrariesMock.mockReturnValue({ data: undefined, isSuccess: false });
});

describe("PlaybackSettings layout", () => {
  it("manages the playback key family and leaves downloads to their own page", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    renderToStaticMarkup(<Page />);
    const keys: string[] = useSettingsFormMock.mock.calls[0]?.[0]?.keys ?? [];

    expect(keys).toContain("playback.transcode_enabled");
    expect(keys).toContain("playback.allow_hevc_encoding");
    expect(keys).toContain("playback.watched_threshold");
    expect(keys).toContain("playback.copy_keyframe_playlist");
    expect(keys.some((key) => key.startsWith("download."))).toBe(false);
    // Node routing and subtitle sync have their own pages and forms.
    expect(keys.some((key) => key.startsWith("playback.routing."))).toBe(false);
    expect(keys.some((key) => key.startsWith("subtitles."))).toBe(false);
    // Hidden tier: still saved and readable through the API, no UI.
    expect(keys).not.toContain("playback.chapter_thumbnail_node_capacity");
  });

  it("orders the groups with preview images between transcoding and watch behavior", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    const container = parse(renderToStaticMarkup(<Page />));

    expect(Array.from(container.querySelectorAll("h3"), (h) => h.textContent)).toEqual([
      "Transcoding",
      "Preview images",
      "Watch behavior",
    ]);
  });

  it("counts each group's advanced settings in its own disclosure", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    render(<Page />);

    // Without a device picker or throttling: HEVC, two paths, two
    // tone-mapping switches, throttle and back buffer.
    expect(
      within(screen.getByRole("group", { name: "Transcoding" })).getByRole("button", {
        name: "Advanced · 7 settings",
      }),
    ).toBeInTheDocument();
    // With a transcode node the placement selects sit outside: two worker
    // counts and the two chapter-thumbnail HDR fields stay.
    expect(
      within(screen.getByRole("group", { name: "Preview images" })).getByRole("button", {
        name: "Advanced · 4 settings",
      }),
    ).toBeInTheDocument();
  });

  it("counts buffer ahead only while throttling is on", () => {
    expandAdvanced();
    useSettingsFormMock.mockReturnValue(
      makeForm({ "playback.hw_accel": "none", enable_transcode_throttle: "true" }),
    );

    render(<Page />);
    const transcoding = within(screen.getByRole("group", { name: "Transcoding" }));

    expect(transcoding.getByRole("button", { name: "Advanced · 8 settings" })).toBeInTheDocument();
    expect(transcoding.getByText("Buffer ahead")).toBeInTheDocument();
  });

  it("keeps HEVC encoding inside the Transcoding disclosure", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    const view = render(<Page />);
    const transcoding = within(screen.getByRole("group", { name: "Transcoding" }));

    expect(transcoding.getByRole("switch", { name: "Video transcoding" })).toBeInTheDocument();
    expect(transcoding.getByRole("switch", { name: "Allow 4K transcoding" })).toBeInTheDocument();
    expect(transcoding.queryByRole("switch", { name: "Allow HEVC encoding" })).toBeNull();
    view.unmount();

    expandAdvanced();
    render(<Page />);
    expect(
      within(screen.getByRole("group", { name: "Transcoding" })).getByRole("switch", {
        name: "Allow HEVC encoding",
      }),
    ).toBeInTheDocument();
  });

  it("opens the Transcoding disclosure for a dirty HEVC switch", () => {
    useSettingsFormMock.mockReturnValue(
      makeForm({ "playback.hw_accel": "none", "playback.allow_hevc_encoding": "true" }, [
        "playback.allow_hevc_encoding",
      ]),
    );

    render(<Page />);

    expect(screen.getByRole("switch", { name: "Allow HEVC encoding" })).toBeChecked();
  });

  it("describes how a blank FFmpeg path is resolved", () => {
    expandAdvanced();
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    render(<Page />);

    expect(
      screen.getByText(
        "Leave blank to use jellyfin-ffmpeg at /usr/lib/jellyfin-ffmpeg/ffmpeg when it is installed, then ffmpeg on PATH. macOS tries Homebrew's ffmpeg-full first.",
      ),
    ).toBeInTheDocument();
  });

  it("keeps preview image fields out of the Transcoding disclosure", () => {
    expandAdvanced();
    expandPreviewImages();
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    render(<Page />);
    const transcoding = within(screen.getByRole("group", { name: "Transcoding" }));
    const previews = within(screen.getByRole("group", { name: "Preview images" }));

    for (const label of [
      "Chapter thumbnail workers",
      "Generate chapter thumbnails on",
      "HDR handling",
      TONE_MAP_LABEL,
      "Preview image width",
      "Seek preview interval",
      "Seek preview workers",
      "Generate seek previews on",
    ]) {
      expect(previews.getByText(label)).toBeInTheDocument();
      expect(transcoding.queryByText(label)).toBeNull();
    }
    expect(transcoding.getByText("Transcode back buffer")).toBeInTheDocument();
  });

  it("opens the Preview images disclosure, not Transcoding's, for a dirty preview field", () => {
    useSettingsFormMock.mockReturnValue(
      makeForm({ "playback.hw_accel": "none", "playback.trickplay_workers": "4" }, [
        "playback.trickplay_workers",
      ]),
    );

    render(<Page />);

    expect(
      within(screen.getByRole("group", { name: "Preview images" })).getByLabelText(
        "Seek preview workers",
      ),
    ).toHaveValue(4);
    expect(
      within(screen.getByRole("group", { name: "Transcoding" })).queryByText("FFmpeg path"),
    ).toBeNull();
  });
});

describe("PlaybackSettings preview images tiering", () => {
  it("points at the per-library switches", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    render(<Page />);
    const group = within(screen.getByRole("group", { name: "Preview images" }));

    expect(
      group.getByText(
        "Chapter thumbnails and seek previews are turned on for each library in Libraries.",
      ),
    ).toBeInTheDocument();
    expect(group.getByRole("link", { name: "Libraries" })).toHaveAttribute(
      "href",
      "/admin/libraries",
    );
  });

  it("shows width and interval without opening the disclosure", () => {
    useSettingsFormMock.mockReturnValue(
      makeForm({
        "playback.hw_accel": "none",
        "playback.preview_image_width": "300",
        "playback.trickplay_interval_seconds": "10",
      }),
    );

    render(<Page />);
    const group = within(screen.getByRole("group", { name: "Preview images" }));

    expect(group.getByRole("button", { name: /^Advanced/ })).toHaveAttribute(
      "aria-expanded",
      "false",
    );
    expect(group.getByLabelText("Preview image width")).toHaveValue(300);
    expect(group.getByLabelText("Seek preview interval")).toHaveValue(10);
    for (const label of [
      "Chapter thumbnail workers",
      "Seek preview workers",
      "HDR handling",
      TONE_MAP_LABEL,
    ]) {
      expect(group.queryByText(label)).toBeNull();
    }
  });

  it("shows where previews are made only while a transcode node can take the work", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    const view = render(<Page />);
    let group = within(screen.getByRole("group", { name: "Preview images" }));
    expect(group.getByText("Generate chapter thumbnails on")).toBeInTheDocument();
    expect(group.getByText("Generate seek previews on")).toBeInTheDocument();
    view.unmount();

    for (const nodes of [
      { data: [], isSuccess: true },
      { data: [transcodeNode({ healthy: false })], isSuccess: true },
      { data: undefined, isSuccess: false },
    ]) {
      useAdminNodesMock.mockReturnValue(nodes);
      const collapsed = render(<Page />);
      group = within(screen.getByRole("group", { name: "Preview images" }));
      expect(group.queryByText("Generate chapter thumbnails on")).toBeNull();
      expect(group.queryByText("Generate seek previews on")).toBeNull();
      // Both selects join the two worker counts and the two HDR fields.
      expect(group.getByRole("button", { name: "Advanced · 6 settings" })).toBeInTheDocument();
      collapsed.unmount();
    }
  });

  it("opens the disclosure for a dirty placement select it holds", () => {
    useAdminNodesMock.mockReturnValue({ data: [], isSuccess: true });
    useSettingsFormMock.mockReturnValue(
      makeForm({ "playback.hw_accel": "none", "playback.chapter_thumbnail_execution": "local" }, [
        "playback.chapter_thumbnail_execution",
      ]),
    );

    render(<Page />);

    expect(
      within(screen.getByRole("group", { name: "Preview images" })).getByText(
        "Generate chapter thumbnails on",
      ),
    ).toBeInTheDocument();
  });

  it("labels the HDR fields as chapter thumbnail settings", () => {
    expandPreviewImages();
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    render(<Page />);
    const group = within(screen.getByRole("group", { name: "Preview images" }));

    expect(group.getByRole("heading", { name: "HDR chapter thumbnails" })).toBeInTheDocument();
    expect(group.getByText("Seek previews always convert HDR to SDR.")).toBeInTheDocument();
  });
});

describe("PlaybackSettings CPU tone mapping", () => {
  it("includes the setting and renders it off by default", () => {
    expandPreviewImages();
    useSettingsFormMock.mockReturnValue(
      makeForm({
        "playback.hw_accel": "none",
        "playback.chapter_thumbnail_hdr_policy": "best_effort",
      }),
    );

    const container = parse(renderToStaticMarkup(<Page />));

    expect(useSettingsFormMock.mock.calls[0]?.[0]?.keys).toContain(
      "playback.chapter_thumbnail_software_tone_map_enabled",
    );
    const toggle = labelled(groupNamed(container, "Preview images"), TONE_MAP_LABEL);
    expect(toggle).toHaveAttribute("aria-checked", "false");
    expect(toggle).not.toHaveAttribute("disabled");
  });

  it("offers VideoToolbox hardware acceleration", async () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "auto" }));

    render(<Page />);
    await userEvent.click(screen.getByRole("combobox", { name: "Hardware acceleration" }));

    expect(screen.getByRole("option", { name: "VideoToolbox (macOS)" })).toBeInTheDocument();
  });

  it("disables the toggle while HDR chapter thumbnails are disabled", () => {
    expandPreviewImages();
    useSettingsFormMock.mockReturnValue(
      makeForm({
        "playback.hw_accel": "none",
        "playback.chapter_thumbnail_hdr_policy": "disabled",
        "playback.chapter_thumbnail_software_tone_map_enabled": "true",
      }),
    );

    const container = parse(renderToStaticMarkup(<Page />));
    const toggle = labelled(groupNamed(container, "Preview images"), TONE_MAP_LABEL);

    expect(toggle).toHaveAttribute("aria-checked", "true");
    expect(toggle).toHaveAttribute("disabled");
  });
});

describe("PlaybackSettings transcode tone mapping", () => {
  beforeEach(expandAdvanced);

  it("registers independent hardware and software settings disabled by default", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "auto" }));

    const container = parse(renderToStaticMarkup(<Page />));
    const keys = useSettingsFormMock.mock.calls[0]?.[0]?.keys as string[];

    expect(keys).toContain("playback.transcode_hardware_tone_map_enabled");
    expect(keys).toContain("playback.transcode_software_tone_map_enabled");
    const transcoding = groupNamed(container, "Transcoding");
    expect(labelled(transcoding, "Enable Hardware HDR Tone Mapping")).toHaveAttribute(
      "aria-checked",
      "false",
    );
    expect(labelled(transcoding, "Enable Software HDR Tone Mapping")).toHaveAttribute(
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

    const container = parse(renderToStaticMarkup(<Page />));

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
      parse(renderToStaticMarkup(<Page />)),
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
      parse(renderToStaticMarkup(<Page />)),
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

    const container = parse(renderToStaticMarkup(<Page />));

    expect(labelled(container, "Transcode directory")).toHaveAttribute(
      "placeholder",
      "/tmp/silo-transcode",
    );
    expect(labelled(container, "FFmpeg path")).toHaveAttribute(
      "placeholder",
      "/usr/lib/jellyfin-ffmpeg/ffmpeg",
    );
  });

  it("stages an empty value when an overridden path is reset", () => {
    const form = makeForm({
      "playback.hw_accel": "none",
      "playback.transcode_dir": "/mnt/fast/transcode",
    });
    useSettingsFormMock.mockReturnValue(form);
    render(<Page />);

    fireEvent.click(screen.getByRole("button", RESET_TRANSCODE_DIR));

    expect(form.setValue).toHaveBeenCalledWith("playback.transcode_dir", "");
    expect(form.save).not.toHaveBeenCalled();
  });
});

describe("PlaybackSettings chapter thumbnail execution", () => {
  beforeEach(expandPreviewImages);

  it("warns when no transcode node can take an extraction", () => {
    useAdminNodesMock.mockReturnValue({
      data: [transcodeNode({ healthy: false }), transcodeNode({ id: 2, type: "streaming" })],
      isSuccess: true,
    });
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    const container = parse(renderToStaticMarkup(<Page />));

    expect(groupNamed(container, "Preview images").textContent).toContain(
      "No transcode nodes are connected",
    );
  });

  it("stays quiet while a healthy transcode node is connected", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    const container = parse(renderToStaticMarkup(<Page />));

    expect(container.textContent).not.toContain("No transcode nodes are connected");
  });

  it("does not warn before the node list has loaded", () => {
    useAdminNodesMock.mockReturnValue({ data: undefined, isSuccess: false });
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));

    const container = parse(renderToStaticMarkup(<Page />));

    expect(container.textContent).not.toContain("No transcode nodes are connected");
  });
});

describe("PlaybackSettings seek-preview node capability", () => {
  beforeEach(expandPreviewImages);

  it.each([
    [{ capabilities: undefined }, true],
    [{ capabilities: { transport_features: ["chapter_extract_v1"] } }, true],
    [{ capabilities: { transport_features: ["trickplay_extract_v1"] } }, true],
    [
      { capabilities: { transport_features: ["trickplay_extract_v1", "trickplay_extract_v2"] } },
      false,
    ],
    [
      {
        capabilities: { transport_features: ["trickplay_extract_v1", "trickplay_extract_v2"] },
        advertised_capabilities_hash: "",
        capabilities_hash: "snapshot",
      },
      true,
    ],
    [
      {
        capabilities: { transport_features: ["trickplay_extract_v1", "trickplay_extract_v2"] },
        advertised_capabilities_hash: "new-snapshot",
        capabilities_hash: "old-snapshot",
      },
      true,
    ],
    [
      {
        capabilities: { transport_features: ["trickplay_extract_v1", "trickplay_extract_v2"] },
        advertised_capabilities_hash: "snapshot",
      },
      true,
    ],
    [
      {
        capabilities: { transport_features: ["trickplay_extract_v1", "trickplay_extract_v2"] },
        advertised_capabilities_hash: "snapshot",
        capabilities_hash: "snapshot",
      },
      false,
    ],
  ])("gates node execution for node snapshot %o", async (overrides, disabled) => {
    useAdminNodesMock.mockReturnValue({ data: [transcodeNode(overrides)], isSuccess: true });
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.trickplay_execution": "local" }));
    render(<Page />);
    await userEvent.click(screen.getByRole("combobox", { name: "Generate seek previews on" }));
    const option = screen.getByRole("option", { name: "Transcode nodes only" });
    expect(option.getAttribute("aria-disabled") === "true").toBe(disabled);
  });

  it.each([
    ["prefer_transcode_nodes", "Transcode nodes when available", "Transcode nodes only"],
    ["transcode_nodes_only", "Transcode nodes only", "Transcode nodes when available"],
  ])("keeps saved %s editable while the node snapshot is stale", async (mode, saved, other) => {
    useAdminNodesMock.mockReturnValue({
      data: [
        transcodeNode({
          capabilities: { transport_features: ["trickplay_extract_v1", "trickplay_extract_v2"] },
          advertised_capabilities_hash: "new-snapshot",
          capabilities_hash: "old-snapshot",
        }),
      ],
      isSuccess: true,
    });
    useSettingsFormMock.mockReturnValue(makeForm({ "playback.trickplay_execution": mode }));
    render(<Page />);
    await userEvent.click(screen.getByRole("combobox", { name: "Generate seek previews on" }));
    expect(screen.getByRole("option", { name: saved })).not.toHaveAttribute(
      "aria-disabled",
      "true",
    );
    expect(screen.getByRole("option", { name: other })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("option", { name: "This server" })).not.toHaveAttribute(
      "aria-disabled",
      "true",
    );
  });
});

describe("HEVC encoding policy", () => {
  it("saves the HEVC switch through the playback settings form", () => {
    expandAdvanced();
    const form = makeForm({ "playback.hw_accel": "none", "playback.allow_hevc_encoding": "false" });
    useSettingsFormMock.mockReturnValue(form);
    render(<Page />);
    fireEvent.click(screen.getByRole("switch", { name: "Allow HEVC encoding" }));
    expect(form.setValue).toHaveBeenCalledWith("playback.allow_hevc_encoding", "true");
  });
});

describe("seek preview settings", () => {
  const library = (ready: number, running = 0) => ({
    library_id: "1",
    name: "Movies",
    pending: 0,
    running,
    ready,
    unusable: 0,
    sheet_bytes: 0,
  });

  it.each([false, true])("handles confirmed preview saves (failure=%s)", async (failure) => {
    const form = makeForm(
      { "playback.hw_accel": "none", "playback.trickplay_interval_seconds": "20" },
      ["playback.trickplay_interval_seconds"],
      { "playback.trickplay_interval_seconds": "10" },
    );
    if (failure) form.save.mockRejectedValue(new Error("412 precondition failed"));
    useSettingsFormMock.mockReturnValue(form);
    useAdminTrickplayLibrariesMock.mockReturnValue({
      data: [{ ...library(0), pending: 3, sheet_bytes: 1000 }],
      isSuccess: true,
    });
    render(<Page />);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(form.save).not.toHaveBeenCalled();
    expect(screen.getByRole("alertdialog")).toBeTruthy();
    await userEvent.click(
      within(screen.getByRole("alertdialog")).getByRole("button", { name: "Save" }),
    );
    expect(form.save).toHaveBeenCalledOnce();
    expect(form.dirtyCount).toBe(1);
  });

  it("manages the four seek preview keys under Preview images", () => {
    expandPreviewImages();
    useSettingsFormMock.mockReturnValue(
      makeForm({
        "playback.hw_accel": "none",
        "playback.preview_image_width": "300",
        "playback.trickplay_interval_seconds": "10",
        "playback.trickplay_workers": "2",
      }),
    );

    render(<Page />);
    const keys: string[] = useSettingsFormMock.mock.calls[0]?.[0]?.keys ?? [];

    for (const key of [
      "playback.preview_image_width",
      "playback.trickplay_interval_seconds",
      "playback.trickplay_workers",
      "playback.trickplay_execution",
    ]) {
      expect(keys).toContain(key);
    }
    const group = within(screen.getByRole("group", { name: "Preview images" }));
    expect(group.getByLabelText("Preview image width")).toHaveValue(300);
    expect(group.getByLabelText("Seek preview interval")).toHaveValue(10);
    expect(group.getByLabelText("Seek preview workers")).toHaveValue(2);
    expect(group.getByText("Generate seek previews on")).toBeTruthy();
  });

  it("asks before a new width remakes chapter thumbnails and published previews", async () => {
    const form = makeForm(
      { "playback.hw_accel": "none", "playback.preview_image_width": "320" },
      ["playback.preview_image_width"],
      { "playback.preview_image_width": "" },
    );
    useSettingsFormMock.mockReturnValue(form);
    useAdminTrickplayLibrariesMock.mockReturnValue({
      data: [library(40, 1), library(2)],
      isSuccess: true,
    });

    render(<Page />);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(form.save).not.toHaveBeenCalled();
    expect(
      screen.getByText(
        /^Every chapter thumbnail and 43 files' seek previews are made again at the new width\./,
      ),
    ).toBeTruthy();
    await userEvent.click(
      within(screen.getByRole("alertdialog")).getByRole("button", { name: "Save" }),
    );
    expect(form.save).toHaveBeenCalledTimes(1);
  });

  it("asks before a new width even with no seek previews, for chapter thumbnails", async () => {
    const form = makeForm(
      { "playback.hw_accel": "none", "playback.preview_image_width": "320" },
      ["playback.preview_image_width"],
      { "playback.preview_image_width": "300" },
    );
    useSettingsFormMock.mockReturnValue(form);
    useAdminTrickplayLibrariesMock.mockReturnValue({ data: [], isSuccess: true });

    render(<Page />);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(form.save).not.toHaveBeenCalled();
    expect(
      screen.getByText(/^Every chapter thumbnail is made again at the new width\./),
    ).toBeTruthy();
  });

  it.each([
    [
      "the interval returns to the default",
      { "playback.trickplay_interval_seconds": "10" },
      ["playback.trickplay_interval_seconds"],
      [library(5)],
    ],
    [
      "a new interval has no published previews to remake",
      { "playback.trickplay_interval_seconds": "20" },
      ["playback.trickplay_interval_seconds"],
      [],
    ],
    [
      "only the worker count changes",
      { "playback.trickplay_workers": "4" },
      ["playback.trickplay_workers"],
      [library(5)],
    ],
  ])("saves without asking when %s", async (_, values, dirty, libraries) => {
    const form = makeForm({ "playback.hw_accel": "none", ...values }, dirty, {
      "playback.trickplay_interval_seconds": "",
      "playback.preview_image_width": "300",
      "playback.trickplay_workers": "1",
    });
    useSettingsFormMock.mockReturnValue(form);
    useAdminTrickplayLibrariesMock.mockReturnValue({ data: libraries, isSuccess: true });

    render(<Page />);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(form.save).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });
});

it("hides unsupported seek preview settings and leaves their keys out", () => {
  expandPreviewImages();
  useLibraryCapabilitiesMock.mockReturnValue({ data: { trickplay: false } });
  useSettingsFormMock.mockReturnValue(makeForm({ "playback.hw_accel": "none" }));
  render(<Page />);
  expect(screen.queryByLabelText("Seek preview interval")).toBeNull();
  expect(screen.queryByLabelText("Preview image width")).toBeNull();
  expect(useSettingsFormMock.mock.calls[0]?.[0]?.keys).not.toContain("playback.trickplay_workers");
  // The chapter thumbnail half of the group stays, and the count follows it:
  // workers and the two HDR fields, with placement outside beside a node.
  const group = within(screen.getByRole("group", { name: "Preview images" }));
  expect(group.getByRole("button", { name: "Advanced · 3 settings" })).toBeInTheDocument();
  expect(group.getByText("Generate chapter thumbnails on")).toBeInTheDocument();
  expect(group.queryByText("Generate seek previews on")).toBeNull();
  expect(group.queryByText("Seek preview workers")).toBeNull();
  expect(
    group.getByText("Chapter thumbnails are turned on for each library in Libraries."),
  ).toBeTruthy();
});

it("asks before changing the interval when preview status is unavailable", async () => {
  useAdminTrickplayLibrariesMock.mockReturnValue({ data: undefined, isSuccess: false });
  const form = makeForm(
    { "playback.trickplay_interval_seconds": "20" },
    ["playback.trickplay_interval_seconds"],
    { "playback.trickplay_interval_seconds": "10" },
  );
  useSettingsFormMock.mockReturnValue(form);
  render(<Page />);
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  expect(form.save).not.toHaveBeenCalled();
  expect(screen.getByRole("alertdialog")).toHaveTextContent(
    "Existing seek previews are made again",
  );
});

// The save bar moves to components/ but must render the same markup on every
// settings page that uses it: scroll room, scrim and the pill.
it("renders the settings save bar for a dirty form", () => {
  useSettingsFormMock.mockReturnValue(
    makeForm({ "playback.hw_accel": "none" }, ["playback.hw_accel", "playback.ffmpeg_path"]),
  );
  render(<Page />);

  const pill = screen.getByText("2 unsaved changes").closest("[role=status]");
  const scrim = pill?.previousElementSibling;
  expect([scrim?.previousElementSibling, scrim, pill]).toMatchInlineSnapshot(`
    [
      <div
        aria-hidden="true"
        class="h-28"
      />,
      <div
        aria-hidden="true"
        class="pointer-events-none fixed right-0 bottom-0 left-0 z-30 h-40 bg-gradient-to-t from-[var(--background)] via-[color-mix(in_srgb,var(--background)_72%,transparent)] to-transparent lg:left-[240px]"
      />,
      <div
        class="pointer-events-none fixed right-0 bottom-6 left-0 z-40 flex justify-center px-4 lg:left-[240px]"
        role="status"
      >
        <div
          class="glass pointer-events-auto flex max-w-full items-center gap-3 rounded-full py-2 pr-2 pl-4 shadow-2xl backdrop-blur-xl sm:gap-4 sm:pl-5"
        >
          <span
            class="min-w-0 truncate text-[13px] font-medium"
          >
            2 unsaved changes
          </span>
          <span
            class="flex shrink-0 items-center gap-1.5"
          >
            <button
              class="inline-flex shrink-0 items-center justify-center text-sm font-medium whitespace-nowrap transition-colors outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4 duration-150 hover:bg-accent hover:text-accent-foreground h-8 gap-1.5 px-3 has-[>svg]:px-2.5 rounded-full"
              data-size="sm"
              data-slot="button"
              data-variant="ghost"
            >
              Discard
            </button>
            <button
              class="inline-flex shrink-0 items-center justify-center text-sm font-medium whitespace-nowrap transition-colors outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4 shadow-sm duration-150 h-8 gap-1.5 px-3 has-[>svg]:px-2.5 rounded-full bg-[var(--settings-accent)] text-[#15151a] hover:bg-[var(--settings-accent)] hover:brightness-110"
              data-size="sm"
              data-slot="button"
              data-variant="default"
            >
              Save
            </button>
          </span>
        </div>
      </div>,
    ]
  `);
});
