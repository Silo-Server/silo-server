import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { SubtitlesStep } from "./SubtitlesStep";

const wizardMock = vi.fn();
vi.mock("../WizardContext", () => ({ useWizardContext: () => wizardMock() }));

const updateMock = vi.fn();
// Keep `data` the same object across renders, as React Query does. A new one
// re-runs the step's load effect, whose cleanup cancels an editor read still
// in flight and leaves the row loading.
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

vi.mock("@/api/v2/adminSubtitleProviderConfiguration", () => ({
  captureProviderEditIntent: (provider: string) => ({ provider }),
  providerIntentActive: () => true,
  getProviderEditor: (intent: { provider: string }) =>
    Promise.resolve({
      intent,
      etag: '"1"',
      body: {
        provider_name: intent.provider,
        enabled: false,
        has_api_key: false,
        has_credentials: false,
      },
    }),
}));

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver ??= ResizeObserverStub as unknown as typeof ResizeObserver;

describe("SubtitlesStep", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    wizardMock.mockReturnValue({ markDone: vi.fn(), setSummary: vi.fn() });
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
});
