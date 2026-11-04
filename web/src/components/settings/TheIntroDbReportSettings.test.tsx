// @vitest-environment jsdom

import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";

// Radix Switch reads element sizes and uses pointer capture; jsdom provides neither.
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
if (typeof globalThis.ResizeObserver === "undefined") {
  (globalThis as unknown as { ResizeObserver: typeof ResizeObserverStub }).ResizeObserver =
    ResizeObserverStub;
}
if (typeof window !== "undefined" && !window.HTMLElement.prototype.hasPointerCapture) {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.scrollIntoView = () => {};
}

import { TheIntroDbReportSettings } from "./TheIntroDbReportSettings";

const deps = vi.hoisted(() => ({
  installation: vi.fn(),
  values: vi.fn(),
  mutate: vi.fn(),
}));

vi.mock("@/hooks/queries/pluginSettings", () => ({
  usePluginInstallation: () => ({ installation: deps.installation() }),
  usePluginSettingsValues: () => deps.values(),
  useUpdatePluginSettings: () => ({ mutate: deps.mutate, isPending: false }),
}));

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

function installed() {
  return {
    id: 4,
    plugin_id: "silo.theintrodb",
    user_config_schema: [{ key: "report" }],
    configured_global_keys: ["account"],
  };
}

beforeEach(() => {
  deps.installation.mockReset().mockReturnValue(installed());
  deps.values.mockReset();
  deps.mutate.mockReset();
});

it("will not write until the account's current values are known", () => {
  // The PUT replaces the whole stored set, so merging onto an unknown one would delete
  // every other entry this account has stored for the plugin.
  deps.values.mockReturnValue({ data: undefined, isSuccess: false, isError: false });

  render(<TheIntroDbReportSettings />);
  const toggle = screen.getByRole("switch");

  expect(toggle).toBeDisabled();
  fireEvent.click(toggle);
  expect(deps.mutate).not.toHaveBeenCalled();
});

it("keeps every other stored value when it writes", () => {
  const existing = {
    report: '{"show_report":false}',
    something_else: '{"kept":true}',
  };
  deps.values.mockReturnValue({ data: existing, isSuccess: true, isError: false });

  render(<TheIntroDbReportSettings />);
  fireEvent.click(screen.getByRole("switch"));

  expect(deps.mutate).toHaveBeenCalledTimes(1);
  const call = deps.mutate.mock.calls[0];
  if (!call) throw new Error("expected a write");
  const written = call[0] as Record<string, string | undefined>;
  expect(written["something_else"]).toBe('{"kept":true}');
  expect(JSON.parse(written["report"] ?? "{}")).toEqual({ show_report: true });
});

it("says why it cannot be used when the values could not be read", () => {
  deps.values.mockReturnValue({ data: undefined, isSuccess: false, isError: true });

  render(<TheIntroDbReportSettings />);

  expect(screen.getByRole("switch")).toBeDisabled();
  expect(screen.getByText(/could not load/i)).toBeTruthy();
});

it("stays off with a reason when the server holds no TheIntroDB key", () => {
  deps.installation.mockReturnValue({ ...installed(), configured_global_keys: [] });
  deps.values.mockReturnValue({ data: undefined, isSuccess: false, isError: false });

  render(<TheIntroDbReportSettings />);

  expect(screen.getByRole("switch")).toBeDisabled();
  expect(screen.getByText(/no TheIntroDB API key/i)).toBeTruthy();
});
