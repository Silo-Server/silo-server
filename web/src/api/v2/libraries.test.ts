import { describe, expect, it } from "vitest";

import getLibraryRealtimeMonitoringOk from "../../../../contracts/api/v2/fixtures/get_library_realtime_monitoring_ok.json";
import listLibrariesOk from "../../../../contracts/api/v2/fixtures/list_libraries_ok.json";

import type { components } from "@/api/v2/schema";

import { libraryCreateToV2, libraryFromV2, libraryRealtimeMonitoringFromV2 } from "./libraries";

type LibraryV2 = components["schemas"]["Library"];

const v2Library = listLibrariesOk.items[0] as LibraryV2;

describe("libraryFromV2", () => {
  it("carries every member of the v2 library", () => {
    // The app type is mapped field by field, so a member the mapper forgets
    // is silently dropped; comparing key sets catches that for new members.
    expect(Object.keys(libraryFromV2(v2Library)).sort()).toEqual(Object.keys(v2Library).sort());
  });

  it.each([true, false])("keeps a realtime_monitoring switch of %s", (realtimeMonitoring) => {
    const library = libraryFromV2({ ...v2Library, realtime_monitoring: realtimeMonitoring });
    expect(library.realtime_monitoring).toBe(realtimeMonitoring);
    expect(library.id).toBe(1);
  });
});

describe("libraryCreateToV2", () => {
  const base = { paths: ["/media/movies"], type: "movies", name: "Movies" };

  it.each([true, false])("sends a realtime_monitoring choice of %s", (realtimeMonitoring) => {
    expect(libraryCreateToV2({ ...base, realtime_monitoring: realtimeMonitoring })).toEqual({
      ...base,
      realtime_monitoring: realtimeMonitoring,
    });
  });

  it("omits realtime_monitoring when the caller leaves it to the server default", () => {
    expect(libraryCreateToV2(base)).not.toHaveProperty("realtime_monitoring");
  });
});

describe("libraryRealtimeMonitoringFromV2", () => {
  it("keys entries by numeric library id and keeps report fields only when present", () => {
    const status = libraryRealtimeMonitoringFromV2(
      getLibraryRealtimeMonitoringOk as components["schemas"]["LibraryRealtimeMonitoring"],
    );

    expect(status.server_enabled).toBe(true);
    expect(status.libraries).toEqual([
      {
        library_id: 1,
        enabled: true,
        state: "monitoring",
        backend: "inotify",
        detail: "",
        directories: 4812,
        node_id: "node-a",
        updated_at: "2026-01-02T03:04:05.678Z",
      },
      {
        library_id: 2,
        enabled: true,
        state: "not_reporting",
        backend: "",
        detail: "No server node can see this library's folders",
        directories: 0,
      },
    ]);
    expect(status.libraries[1]).not.toHaveProperty("node_id");
    expect(status.libraries[1]).not.toHaveProperty("updated_at");
  });
});
