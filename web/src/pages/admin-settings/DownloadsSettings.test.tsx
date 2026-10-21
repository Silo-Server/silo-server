import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import DownloadsSettings from "./DownloadsSettings";
import { fakeSettingsForm } from "./settingsForm.test-support";

const useSettingsFormMock = vi.fn();

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => new Set<string>(["download.artifact_dir"]),
}));

const makeForm = (values: Record<string, string> = {}, dirty: string[] = []) =>
  fakeSettingsForm(values, { dirty });

/** The most recent form the page rendered with, for `setValue` assertions. */
function lastForm() {
  const results = useSettingsFormMock.mock.results;
  return results[results.length - 1]?.value as ReturnType<typeof makeForm>;
}

// The page links to the storage view, so it renders inside a router.
function renderSettings() {
  return render(
    <MemoryRouter>
      <DownloadsSettings />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  localStorage.clear();
  useSettingsFormMock.mockReset();
  useSettingsFormMock.mockReturnValue(makeForm());
});

describe("DownloadsSettings prepared file storage budget", () => {
  it("writes bytes back when a GB budget is typed", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "download.artifact_max_bytes": "53687091200" }));
    renderSettings();

    expect(screen.getByLabelText("Default storage budget per location")).toHaveValue(53.687);
    expect(screen.getByRole("group", { name: "Downloads" }).textContent).not.toContain(
      "53687091200",
    );

    fireEvent.change(screen.getByLabelText("Default storage budget per location"), {
      target: { value: "100" },
    });

    expect(lastForm().setValue).toHaveBeenCalledWith("download.artifact_max_bytes", "100000000000");
  });

  it("keeps unlimited as the default rather than showing 0 GB", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "download.artifact_max_bytes": "0" }));
    renderSettings();

    const input = screen.getByLabelText("Default storage budget per location");

    expect(input).toHaveValue(null);
    expect(input).toHaveAttribute("placeholder", "Unlimited");
  });

  // Neighbouring row, kept honest here: the server reads 0 on this key as
  // "use the built-in worker count", so it must not become an Unlimited box.
  it("keeps 0 meaningful on Files prepared at once", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "download.max_concurrent_prepares": "0" }));
    renderSettings();

    expect(screen.getByLabelText("Files prepared at once")).toHaveValue(0);
    expect(screen.getByText("0 uses the built-in default of 2.")).toBeInTheDocument();
  });
});

describe("DownloadsSettings prepared file directory", () => {
  const RESET = { name: "Reset Prepared file directory to default" };

  it("stages an empty value when an overridden directory is reset", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "download.artifact_dir": "/mnt/downloads" }));
    renderSettings();

    fireEvent.click(screen.getByRole("button", RESET));

    expect(lastForm().setValue).toHaveBeenCalledWith("download.artifact_dir", "");
  });
});

it("shows a failed read without editable defaults and permits retry", () => {
  const form = { ...makeForm(), loaded: false, loadError: true };
  useSettingsFormMock.mockReturnValue(form);
  render(<DownloadsSettings />);
  expect(screen.getByRole("alert")).toHaveTextContent("could not be loaded");
  expect(screen.queryByLabelText("Per-user bandwidth")).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect(form.retryLoad).toHaveBeenCalledOnce();
});
