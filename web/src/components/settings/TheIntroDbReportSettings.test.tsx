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
  setEntryValue: vi.fn(),
}));

vi.mock("@/hooks/queries/pluginSettings", () => ({
  usePluginInstallation: () => ({ installation: deps.installation() }),
  usePluginSettingsValues: () => deps.values(),
  useUpdatePluginEntry: () => ({ setEntryValue: deps.setEntryValue, isPending: false }),
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
  deps.setEntryValue.mockReset();
});

it("will not write until the account's current values are known", () => {
  // The PUT replaces the whole stored set for this account and plugin, so the control
  // stays off until the set is known — including after a failed read, which must not
  // read as "nothing is stored".
  deps.values.mockReturnValue({ data: undefined, isSuccess: false, isError: false });

  render(<TheIntroDbReportSettings />);
  const toggle = screen.getByRole("switch");

  expect(toggle).toBeDisabled();
  fireEvent.click(toggle);
  expect(deps.setEntryValue).not.toHaveBeenCalled();
});

it("writes the one field of the one entry the switch owns", () => {
  // The entry is the unit the settings contract addresses, so the write names the entry
  // and the field, and everything else stored for the account is carried through by the
  // hook's own re-read rather than by the copy this screen happens to hold.
  deps.values.mockReturnValue({
    data: { report: '{"show_report":false}' },
    isSuccess: true,
    isError: false,
  });

  render(<TheIntroDbReportSettings />);
  fireEvent.click(screen.getByRole("switch"));

  expect(deps.setEntryValue).toHaveBeenCalledTimes(1);
  const call = deps.setEntryValue.mock.calls[0];
  if (!call) throw new Error("expected a write");
  expect(call[0]).toBe("report");
  expect(call[1]).toEqual({ show_report: true });
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
