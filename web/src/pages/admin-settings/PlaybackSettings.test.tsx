import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import PlaybackSettings from "./PlaybackSettings";
import { fakeSettingsForm } from "./settingsForm.test-support";

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
const markerCapabilitiesMock = vi.fn<() => { data?: Record<string, boolean> }>(() => ({
  data: { detection_kind_settings: true },
}));

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

vi.mock("@/hooks/queries/admin/markers", () => ({
  useAdminMarkerCapabilities: () => markerCapabilitiesMock(),
}));

vi.mock("@/hooks/queries/admin/tasks", () => ({
  useTasks: () => ({ data: [] }),
  useRunTask: () => ({ mutateAsync: vi.fn() }),
}));

vi.mock("@/components/realtimeEventsContext", () => ({
  useEventChannel: () => undefined,
}));

// Preview images links to Libraries and Skip markers to its providers, so the
// page renders in a router.
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

const makeForm = (
  values: Record<string, string>,
  dirty: string[] = [],
  persisted: Record<string, string> = {},
) => fakeSettingsForm(values, { dirty, persisted, save: vi.fn().mockResolvedValue(undefined) });

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
  markerCapabilitiesMock.mockReturnValue({ data: { detection_kind_settings: true } });
});

describe("PlaybackSettings preview images tiering", () => {
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
      // Both selects join the seek preview worker count and the two HDR fields.
      expect(group.getByRole("button", { name: "Advanced · 5 settings" })).toBeInTheDocument();
      collapsed.unmount();
    }
  });
});

describe("PlaybackSettings CPU tone mapping", () => {
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

describe("PlaybackSettings skip markers", () => {
  function markersGroup(values: Record<string, string>, dirty: string[] = []): HTMLElement {
    useSettingsFormMock.mockReturnValue(
      makeForm({ "playback.hw_accel": "none", ...values }, dirty),
    );
    return groupNamed(parse(renderToStaticMarkup(<Page />)), "Skip markers");
  }

  it("shows the settings that apply to each marker source, without a disclosure", () => {
    const cases: Array<[string, string[], string[]]> = [
      [
        "both",
        [
          "Detect intros",
          "Detect credits",
          "Save online markers",
          "Find markers on playback",
          "Detection workers",
        ],
        [],
      ],
      [
        "local",
        ["Detect intros", "Detect credits", "Find markers on playback", "Detection workers"],
        ["Save online markers"],
      ],
      [
        "online",
        ["Save online markers", "Find markers on playback"],
        ["Detect intros", "Detect credits", "Detection workers"],
      ],
      [
        "off",
        [],
        [
          "Detect intros",
          "Detect credits",
          "Save online markers",
          "Find markers on playback",
          "Detection workers",
        ],
      ],
    ];
    for (const [mode, shown, hidden] of cases) {
      const group = markersGroup({ "markers.mode": mode });
      expect(group.textContent).toContain("Marker source");
      for (const label of shown) expect(group.textContent).toContain(label);
      for (const label of hidden) expect(group.textContent).not.toContain(label);
      expect(group.querySelector("button[aria-label^=Advanced]")).toBeNull();
    }
  });

  it("leaves out playback lookups while online markers are fetched when needed", () => {
    const group = markersGroup({ "markers.mode": "online", "markers.online_storage": "on_demand" });

    expect(group.textContent).toContain("Save online markers");
    expect(group.textContent).not.toContain("Find markers on playback");
  });

  it("hides the detection switches unless the server honors them", () => {
    const cases: Array<{ data?: Record<string, boolean> }> = [
      {},
      { data: { redetect_markers: true } },
      { data: { detection_kind_settings: false } },
    ];
    for (const capabilities of cases) {
      markerCapabilitiesMock.mockReturnValue(capabilities);
      const group = markersGroup({ "markers.mode": "local" });
      expect(group.textContent).not.toContain("Detect intros");
      expect(group.textContent).not.toContain("Detect credits");
      expect(group.textContent).toContain("Detection workers");
    }
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
  // the two HDR fields, with workers shown and placement outside beside a node.
  const group = within(screen.getByRole("group", { name: "Preview images" }));
  expect(group.getByRole("button", { name: "Advanced · 2 settings" })).toBeInTheDocument();
  expect(group.getByText("Chapter thumbnail workers")).toBeInTheDocument();
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
