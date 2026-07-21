import { describe, expect, it } from "vitest";
import {
  buildHWDeviceRows,
  nodeInventoriesDiverge,
  parseHWDeviceList,
  toggleHWDevice,
} from "./playbackSettings.utils";

const DETECTED = ["/dev/dri/renderD128", "/dev/dri/renderD129"];

describe("parseHWDeviceList", () => {
  it("returns empty for unset or blank values", () => {
    expect(parseHWDeviceList(undefined)).toEqual([]);
    expect(parseHWDeviceList("")).toEqual([]);
    expect(parseHWDeviceList(" , ")).toEqual([]);
  });

  it("splits and trims a comma list", () => {
    expect(parseHWDeviceList(" /dev/dri/renderD128 ,/dev/dri/renderD129")).toEqual(DETECTED);
  });
});

describe("toggleHWDevice", () => {
  it("adds a device to an empty selection", () => {
    expect(toggleHWDevice("", "/dev/dri/renderD129", DETECTED)).toBe("/dev/dri/renderD129");
  });

  it("keeps detection order regardless of click order", () => {
    const afterSecond = toggleHWDevice("", "/dev/dri/renderD129", DETECTED);
    const afterBoth = toggleHWDevice(afterSecond, "/dev/dri/renderD128", DETECTED);
    expect(afterBoth).toBe("/dev/dri/renderD128,/dev/dri/renderD129");
  });

  it("removes an already-selected device", () => {
    expect(
      toggleHWDevice("/dev/dri/renderD128,/dev/dri/renderD129", "/dev/dri/renderD128", DETECTED),
    ).toBe("/dev/dri/renderD129");
  });

  it("preserves selected devices missing from the current detection pass", () => {
    expect(toggleHWDevice("/dev/dri/renderD200", "/dev/dri/renderD128", DETECTED)).toBe(
      "/dev/dri/renderD128,/dev/dri/renderD200",
    );
  });
});

describe("buildHWDeviceRows", () => {
  const detection = (overrides: object) =>
    ({
      resolved: "qsv",
      render_devices: DETECTED,
      render_device_details: DETECTED.map((path) => ({ path, description: "Intel GPU" })),
      intel_detected: true,
      source: "local",
      ...overrides,
    }) as never;

  it("keeps configured devices visible when detection returns nothing", () => {
    const rows = buildHWDeviceRows(
      detection({ render_devices: [], render_device_details: [] }),
      "/dev/dri/renderD128,/dev/dri/renderD129",
    );
    expect(rows).toHaveLength(2);
    expect(rows.every((row) => !row.detected)).toBe(true);
    expect(rows.map((row) => row.path)).toEqual(DETECTED);
  });

  it("keeps configured devices visible with no detection data at all", () => {
    const rows = buildHWDeviceRows(undefined, "/dev/dri/renderD128");
    expect(rows).toEqual([
      {
        path: "/dev/dri/renderD128",
        description: "Configured device not detected",
        detected: false,
        missingOnNodes: [],
      },
    ]);
  });

  it("falls back to render_devices paths when an older node omits details", () => {
    const rows = buildHWDeviceRows(detection({ render_device_details: undefined }), "");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({ path: DETECTED[0], description: "GPU", detected: true });
  });

  it("merges detected and configured-but-missing devices", () => {
    const rows = buildHWDeviceRows(detection({}), "/dev/dri/renderD200");
    expect(rows).toHaveLength(3);
    expect(rows[2]).toMatchObject({ path: "/dev/dri/renderD200", detected: false });
  });

  it("marks devices missing from responding nodes", () => {
    const rows = buildHWDeviceRows(
      detection({
        nodes: [
          { node_url: "http://a", node_name: "node-a", render_devices: DETECTED },
          { node_url: "http://b", node_name: "node-b", render_devices: [DETECTED[0]] },
          { node_url: "http://c", node_name: "node-down", error: "boom" },
        ],
      }),
      "",
    );
    expect(rows.map((row) => row.missingOnNodes)).toEqual([[], ["node-b"]]);
  });
});

describe("nodeInventoriesDiverge", () => {
  const base = {
    resolved: "qsv",
    render_devices: [],
    intel_detected: false,
    source: "local",
  } as never;

  it("is false without nodes or with one node", () => {
    expect(nodeInventoriesDiverge(undefined)).toBe(false);
    expect(nodeInventoriesDiverge({ ...(base as object), nodes: [] } as never)).toBe(false);
    expect(
      nodeInventoriesDiverge({
        ...(base as object),
        nodes: [{ node_url: "http://a", render_devices: DETECTED }],
      } as never),
    ).toBe(false);
  });

  it("is false when responding nodes match (order-insensitive), ignoring failed nodes", () => {
    expect(
      nodeInventoriesDiverge({
        ...(base as object),
        nodes: [
          { node_url: "http://a", render_devices: [DETECTED[0], DETECTED[1]] },
          { node_url: "http://b", render_devices: [DETECTED[1], DETECTED[0]] },
          { node_url: "http://c", error: "boom" },
        ],
      } as never),
    ).toBe(false);
  });

  it("is true when responding nodes report different devices", () => {
    expect(
      nodeInventoriesDiverge({
        ...(base as object),
        nodes: [
          { node_url: "http://a", render_devices: DETECTED },
          { node_url: "http://b", render_devices: [DETECTED[0]] },
        ],
      } as never),
    ).toBe(true);
  });
});
