import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import DownloadsSettings from "./DownloadsSettings";

const useSettingsFormMock = vi.fn();

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => new Set<string>(["download.artifact_dir"]),
}));

function makeForm(values: Record<string, string> = {}, dirty: string[] = []) {
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
    sensitiveConfigured: [],
    sensitiveManagedByEnv: [],
  };
}

/** Opens the page's advanced disclosure via its persisted state. */
function expandAdvanced() {
  localStorage.setItem("silo.admin.advanced.downloads", "true");
}

/** The most recent form the page rendered with, for `setValue` assertions. */
function lastForm() {
  const results = useSettingsFormMock.mock.results;
  return results[results.length - 1]?.value as ReturnType<typeof makeForm>;
}

beforeEach(() => {
  localStorage.clear();
  useSettingsFormMock.mockReset();
  useSettingsFormMock.mockReturnValue(makeForm());
});

describe("DownloadsSettings staged edits", () => {
  it("stages the downloads toggle without saving it", () => {
    render(<DownloadsSettings />);

    expect(useSettingsFormMock.mock.calls[0]?.[0]?.keys).toEqual([
      "download.enabled",
      "download.user_bandwidth_mbps",
      "download.max_concurrent_per_user",
      "download.max_per_period",
      "download.period_duration",
      "download.server_bandwidth_mbps",
      "download.transcode_enabled",
      "download.local_transcode_fallback",
      "download.artifact_dir",
      "download.max_concurrent_prepares",
      "download.artifact_max_bytes",
    ]);

    fireEvent.click(screen.getByRole("switch", { name: /Allow downloads/i }));

    const form = lastForm();
    expect(form.setValue).toHaveBeenCalledWith("download.enabled", "true");
    expect(form.save).not.toHaveBeenCalled();
  });

  it("stages a per-user bandwidth cap", () => {
    render(<DownloadsSettings />);

    fireEvent.change(screen.getByLabelText("Per-user bandwidth"), { target: { value: "25" } });

    expect(lastForm().setValue).toHaveBeenCalledWith("download.user_bandwidth_mbps", "25");
  });

  it("stages an advanced text field", () => {
    expandAdvanced();
    render(<DownloadsSettings />);

    fireEvent.change(screen.getByLabelText("Prepared file directory"), {
      target: { value: "/var/lib/silo/downloads" },
    });

    expect(lastForm().setValue).toHaveBeenCalledWith(
      "download.artifact_dir",
      "/var/lib/silo/downloads",
    );
  });
});

describe("DownloadsSettings prepared file storage budget", () => {
  beforeEach(expandAdvanced);

  it("writes bytes back when a GB budget is typed", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "download.artifact_max_bytes": "53687091200" }));
    render(<DownloadsSettings />);

    expect(screen.getByLabelText("Prepared file storage budget")).toHaveValue(53.687);
    expect(screen.getByRole("group", { name: "Downloads" }).textContent).not.toContain(
      "53687091200",
    );

    fireEvent.change(screen.getByLabelText("Prepared file storage budget"), {
      target: { value: "100" },
    });

    expect(lastForm().setValue).toHaveBeenCalledWith("download.artifact_max_bytes", "100000000000");
  });

  it("keeps unlimited as the default rather than showing 0 GB", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "download.artifact_max_bytes": "0" }));
    render(<DownloadsSettings />);

    const input = screen.getByLabelText("Prepared file storage budget");

    expect(input).toHaveValue(null);
    expect(input).toHaveAttribute("placeholder", "Unlimited");
  });

  // Neighbouring row, kept honest here: the server reads 0 on this key as
  // "use the built-in worker count", so it must not become an Unlimited box.
  it("keeps 0 meaningful on Files prepared at once", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "download.max_concurrent_prepares": "0" }));
    render(<DownloadsSettings />);

    expect(screen.getByLabelText("Files prepared at once")).toHaveValue(0);
    expect(screen.getByText("0 uses the built-in default of 2.")).toBeInTheDocument();
  });
});

describe("DownloadsSettings prepared file directory", () => {
  beforeEach(expandAdvanced);

  const RESET = { name: "Reset Prepared file directory to default" };

  it("stages an empty value when an overridden directory is reset", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "download.artifact_dir": "/mnt/downloads" }));
    render(<DownloadsSettings />);

    fireEvent.click(screen.getByRole("button", RESET));

    expect(lastForm().setValue).toHaveBeenCalledWith("download.artifact_dir", "");
  });
});
