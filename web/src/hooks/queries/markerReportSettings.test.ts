// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { beforeEach, expect, it, vi } from "vitest";

import { useMarkerReportSettings } from "./markerReportSettings";

const plugin = vi.hoisted(() => ({
  installation: vi.fn(),
  values: vi.fn(),
}));

vi.mock("./pluginSettings", () => ({
  usePluginInstallation: (...args: unknown[]) => plugin.installation(...args),
  usePluginSettingsValues: (...args: unknown[]) => plugin.values(...args),
}));

/**
 * The report control is drawn from two halves that belong to different people: the
 * viewer's own switch, and the administrator's TheIntroDB key. Both have to hold,
 * and each has to fail closed on its own.
 */
function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return createElement(QueryClientProvider, { client }, children);
}

function installation(overrides: Record<string, unknown> = {}) {
  return {
    id: 4,
    plugin_id: "silo.theintrodb",
    user_config_schema: [{ key: "report" }],
    configured_global_keys: ["account"],
    ...overrides,
  };
}

/** Runs the hook with the plugin installed as `installation(overrides)`, or absent. */
function run(options: { installation?: unknown } = {}, stored?: string) {
  plugin.installation.mockReturnValue({
    // An explicit null means "not installed", so it must not fall back to the default.
    installation: "installation" in options ? options.installation : installation(),
  });
  plugin.values.mockReturnValue({
    data: stored === undefined ? undefined : { report: stored },
  });
  return renderHook(() => useMarkerReportSettings({ enabled: true }), { wrapper });
}

beforeEach(() => {
  plugin.installation.mockReset();
  plugin.values.mockReset();
});

it("hands the route over once the viewer has switched the control on", () => {
  const { result } = run({}, '{"show_report":true}');

  expect(result.current).toEqual({ available: true, enabled: true, installationId: 4 });
});

it("withholds the route when the viewer has switched the control off", () => {
  const { result } = run({}, '{"show_report":false}');

  expect(result.current.available).toBe(true);
  expect(result.current.enabled).toBe(false);
  // The switch is the only thing deciding whether a button is drawn, so it has to
  // decide this too: an installation id handed over on its own draws the control.
  expect(result.current.installationId).toBeNull();
});

it("withholds the route when nothing is stored for the account yet", () => {
  const { result } = run();

  expect(result.current.enabled).toBe(false);
  expect(result.current.installationId).toBeNull();
});

it("treats a string value as off rather than as consent", () => {
  // A stored "true" is not the boolean the schema declares, and reading it as
  // consent would enable a control the schema says is off.
  const { result } = run({}, '{"show_report":"true"}');

  expect(result.current.installationId).toBeNull();
});

it("is unavailable when the server holds no TheIntroDB key", () => {
  const { result } = run({ installation: installation({ configured_global_keys: [] }) });

  expect(result.current).toEqual({ available: false, enabled: false, installationId: null });
});

it("does not read the account's values when the server holds no key", () => {
  run({ installation: installation({ configured_global_keys: [] }) });

  const [, options] = plugin.values.mock.calls[0] as [unknown, { enabled?: boolean }];
  expect(options.enabled).toBe(false);
});

it("is unavailable when the plugin is not installed", () => {
  const { result } = run({ installation: null });

  expect(result.current).toEqual({ available: false, enabled: false, installationId: null });
});

it("is unavailable when the installed plugin predates the report setting", () => {
  // An older installation has no `report` entry, so nothing could store the value.
  const { result } = run({
    installation: installation({ user_config_schema: [{ key: "account" }] }),
  });

  expect(result.current).toEqual({ available: false, enabled: false, installationId: null });
});
