import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { SubtitlesStep } from "./SubtitlesStep";

const markDone = vi.fn();
vi.mock("../WizardContext", () => ({
  useWizardContext: () => ({ markDone, setSummary: vi.fn() }),
}));

const updateMock = vi.fn();
// One object for every render, as React Query returns stable data; a new one
// each time would restart the step's editor load on every re-render.
const providersQuery = {
  data: {
    providers: [
      { provider_name: "subdl", enabled: false, has_api_key: false, has_credentials: false },
    ],
  },
  isLoading: false,
  isError: false,
  scope: "scope",
  refetch: vi.fn(),
};
vi.mock("@/hooks/queries/admin/subtitles", () => ({
  useSubtitleProviders: () => providersQuery,
  useUpdateSubtitleProvider: () => ({ mutateAsync: updateMock }),
  useTestSubtitleProvider: () => ({ mutate: vi.fn(), isPending: false }),
}));

const editorMock = vi.fn();
vi.mock("@/api/v2/adminSubtitleProviderConfiguration", () => ({
  captureProviderEditIntent: (provider: string) => ({ provider }),
  providerIntentActive: () => true,
  getProviderEditor: (intent: { provider: string }) => editorMock(intent),
}));

function editor(intent: { provider: string }, etag: string, enabled: boolean, hasKey: boolean) {
  return {
    intent,
    etag,
    body: {
      provider_name: intent.provider,
      enabled,
      has_api_key: hasKey,
      has_credentials: false,
    },
  };
}

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver ??= ResizeObserverStub as unknown as typeof ResizeObserver;

describe("SubtitlesStep", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    editorMock.mockImplementation((intent) => Promise.resolve(editor(intent, '"1"', false, false)));
    updateMock.mockResolvedValue({ saved_revision: "2", local_apply: "not_configured" });
  });

  it("leaves out credentials typed for a provider that was switched off again", async () => {
    const user = userEvent.setup();
    render(<SubtitlesStep />);

    const toggle = screen.getByRole("switch", { name: "Enable SubDL" });
    await waitFor(() => expect(toggle).toBeEnabled());
    await user.click(toggle);
    await user.type(screen.getByLabelText("API key"), "typed-key");
    await user.click(toggle);
    expect(screen.queryByLabelText("API key")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Continue" }));

    await waitFor(() => expect(updateMock).toHaveBeenCalledTimes(1));
    expect(updateMock.mock.calls[0]![0].config).toStrictEqual({ enabled: false });
  });

  it("keeps a failed save from being sent again until the admin reloads it", async () => {
    const user = userEvent.setup();
    render(<SubtitlesStep />);

    const toggle = screen.getByRole("switch", { name: "Enable SubDL" });
    await waitFor(() => expect(toggle).toBeEnabled());
    await user.click(toggle);
    await user.type(screen.getByLabelText("API key"), "typed-key");

    // Another admin saves SubDL first, so this save is refused and every
    // later read returns their configuration.
    updateMock.mockRejectedValueOnce(new Error("precondition failed"));
    editorMock.mockImplementation((intent) => Promise.resolve(editor(intent, '"2"', true, true)));
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await waitFor(() => expect(updateMock).toHaveBeenCalledTimes(1));
    const continueButton = await screen.findByRole("button", { name: "Continue" });

    // Continuing again must neither re-send the old draft nor finish the step.
    await user.click(continueButton);
    expect(updateMock).toHaveBeenCalledTimes(1);
    expect(markDone).not.toHaveBeenCalled();
    expect(screen.getByLabelText("API key")).toHaveValue("typed-key");

    // A reload that fails keeps the row locked and leaves the way to try again.
    const reload = screen.getByRole("button", {
      name: "Reload saved settings (discard changes)",
    });
    editorMock.mockRejectedValueOnce(new Error("load failed"));
    await user.click(reload);
    await screen.findByText("Couldn’t load saved settings. Reload before editing.");
    expect(reload).toBeEnabled();
    expect(continueButton).toBeDisabled();
    expect(screen.getByLabelText("API key")).toHaveValue("typed-key");

    // Reloading discards the draft and shows the other admin's saved state.
    await user.click(reload);
    await waitFor(() => expect(screen.getByLabelText("API key")).toHaveValue(""));
    expect(toggle).toBeChecked();
    expect(toggle).toHaveFocus();

    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(markDone).toHaveBeenCalledWith("subtitles");
    expect(updateMock).toHaveBeenCalledTimes(1);
  });
});
