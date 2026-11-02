// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  useAdminServerSettings: vi.fn(),
  useAdminSensitiveStatus: vi.fn(),
  useRecommendationsStatus: vi.fn(),
  useAIProviderModels: vi.fn(),
  checkConnectionMutateAsync: vi.fn(),
  updateMutate: vi.fn(),
  updateMutateAsync: vi.fn(),
  triggerEmbeddingsMutate: vi.fn(),
  triggerTasteProfilesMutate: vi.fn(),
  triggerCowatchMutate: vi.fn(),
  triggerRecommendationsMutate: vi.fn(),
  resetMutateAsync: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("sonner", () => ({
  toast: {
    success: (...args: unknown[]) => mocks.toastSuccess(...args),
    error: (...args: unknown[]) => mocks.toastError(...args),
  },
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerSettings: (...args: unknown[]) => mocks.useAdminServerSettings(...args),
  useCheckAdminSettingsConnection: () => ({
    isPending: false,
    mutateAsync: (...args: unknown[]) => mocks.checkConnectionMutateAsync(...args),
  }),
  useUpdateServerSettings: () => ({
    isPending: false,
    mutate: (...args: unknown[]) => mocks.updateMutate(...args),
    mutateAsync: (...args: unknown[]) => mocks.updateMutateAsync(...args),
  }),
  useAdminSensitiveStatus: (...args: unknown[]) => mocks.useAdminSensitiveStatus(...args),
}));

vi.mock("@/hooks/queries/admin/recommendations", () => ({
  useRecommendationsStatus: (...args: unknown[]) => mocks.useRecommendationsStatus(...args),
  useTriggerEmbeddings: () => ({
    isPending: false,
    mutate: (...args: unknown[]) => mocks.triggerEmbeddingsMutate(...args),
  }),
  useTriggerTasteProfiles: () => ({
    isPending: false,
    mutate: (...args: unknown[]) => mocks.triggerTasteProfilesMutate(...args),
  }),
  useTriggerCowatch: () => ({
    isPending: false,
    mutate: (...args: unknown[]) => mocks.triggerCowatchMutate(...args),
  }),
  useTriggerRecommendations: () => ({
    isPending: false,
    mutate: (...args: unknown[]) => mocks.triggerRecommendationsMutate(...args),
  }),
  useResetEmbeddings: () => ({
    isPending: false,
    mutateAsync: (...args: unknown[]) => mocks.resetMutateAsync(...args),
  }),
}));

vi.mock("@/hooks/queries/admin/aiProviders", () => ({
  useAIProviders: () => ({
    isLoading: false,
    data: {
      providers: [
        {
          id: "box",
          type: "custom",
          name: "Embedding box",
          base_url: "http://localhost:9999",
          has_api_key: true,
          capabilities: ["embeddings"],
          used_by: ["embeddings"],
        },
      ],
      types: [],
    },
  }),
  useAIProviderModels: () => mocks.useAIProviderModels(),
  useRefreshAIProviders: () => vi.fn(),
}));

import AdminRecommendations from "./AdminRecommendations";

function idleJob(count: number) {
  return { running: false, count };
}

function statusWith(overrides: Record<string, unknown> = {}) {
  return {
    embeddings: { ...idleJob(3), total: 10 },
    taste_profiles: idleJob(4),
    cowatch: idleJob(6),
    recommendations: idleJob(5),
    stale_sweep: { pending: 0 },
    lock_conflict: "",
    ...overrides,
  };
}

function findButton(container: HTMLElement, label: string) {
  return Array.from(container.querySelectorAll("button")).find((button) =>
    button.textContent?.includes(label),
  );
}

async function click(element: Element | undefined) {
  if (!element) {
    throw new Error("element not found");
  }

  await act(async () => {
    element.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

async function changeInput(input: HTMLInputElement | null, value: string) {
  if (!input) {
    throw new Error("input not found");
  }

  const descriptor = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value");
  if (!descriptor?.set) {
    throw new Error("input value setter not found");
  }

  await act(async () => {
    descriptor.set?.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

describe("AdminRecommendations", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);

    for (const mock of Object.values(mocks)) mock.mockReset();

    mocks.useAdminServerSettings.mockReturnValue({
      data: {
        "recommendations.enabled": "true",
        "recommendations.embedding_provider_id": "box",
        "recommendations.embedding_model": "custom-model",
      },
      isLoading: false,
    });
    mocks.useAdminSensitiveStatus.mockReturnValue({ data: { configured: [] } });
    mocks.useRecommendationsStatus.mockReturnValue({ data: undefined });
    mocks.useAIProviderModels.mockReturnValue({ data: { models: [] } });
    mocks.checkConnectionMutateAsync.mockResolvedValue({
      success: true,
      message: "Embedding connection successful.",
    });
    mocks.updateMutateAsync.mockResolvedValue({ values: {}, restart_required: true });
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
  });

  async function render() {
    await act(async () => {
      root.render(<AdminRecommendations />);
    });
  }

  it("checks the current unsaved embedding draft", async () => {
    await render();

    const modelInput = within(container).getByLabelText<HTMLInputElement>("Model");
    expect(modelInput?.value).toBe("custom-model");

    await changeInput(modelInput, "draft-model");

    await click(findButton(container, "Check Connection"));

    expect(mocks.checkConnectionMutateAsync).toHaveBeenCalledWith({
      kind: "recommendations_embedding",
      body: {
        values: {
          "recommendations.enabled": "true",
          "recommendations.embedding_provider_id": "box",
          "recommendations.embedding_model": "draft-model",
        },
        dirty_keys: ["recommendations.embedding_model"],
      },
    });
  });

  it.each([false, true])(
    "saves one completed model edit when the picker is cancelled: %s",
    async (cancel) => {
      if (!HTMLElement.prototype.hasPointerCapture)
        HTMLElement.prototype.hasPointerCapture = () => false;
      if (!HTMLElement.prototype.scrollIntoView) HTMLElement.prototype.scrollIntoView = () => {};
      mocks.useAIProviderModels.mockReturnValue({
        data: { models: [{ id: "listed-model", kind: "embeddings" }] },
      });
      let finishSave!: (result: {
        values: Record<string, string>;
        restart_required: boolean;
      }) => void;
      mocks.updateMutateAsync.mockImplementation(
        () =>
          new Promise((resolve) => {
            finishSave = resolve;
          }),
      );
      const user = userEvent.setup();
      await render();
      const field = within(container).getByLabelText<HTMLInputElement>("Model");
      await user.clear(field);
      await user.type(field, "typed-model");
      await user.click(within(container).getByRole("combobox", { name: "Choose model" }));
      if (cancel) await user.keyboard("{Escape}");
      else await user.click(within(document.body).getByRole("option", { name: "listed-model" }));
      expect(mocks.updateMutateAsync).not.toHaveBeenCalled();
      expect(field).toHaveFocus();
      const chosen = cancel ? "typed-model" : "listed-model";
      expect(field).toHaveValue(chosen);
      await user.tab();
      await user.tab();
      expect(mocks.updateMutateAsync).toHaveBeenCalledTimes(1);
      await act(async () =>
        finishSave({
          values: { "recommendations.embedding_model": chosen },
          restart_required: false,
        }),
      );
      expect(field).toHaveValue(chosen);
    },
  );

  it("shows each job's last run, the newest cached row and a lock conflict", async () => {
    const minutesAgo = (n: number) => new Date(Date.now() - n * 60_000).toISOString();
    mocks.useRecommendationsStatus.mockReturnValue({
      data: statusWith({
        embeddings: {
          ...idleJob(0),
          total: 10,
          last_run: {
            status: "failed",
            started_at: minutesAgo(12),
            completed_at: minutesAgo(10),
            error: "recommendations reset required: embedding model changed",
            result: { embedded: 0 },
          },
          last_partial_run: {
            status: "completed",
            started_at: minutesAgo(4),
            completed_at: minutesAgo(3),
            result: { missing_only: true, embedded: 2 },
          },
        },
        recommendations: {
          ...idleJob(5),
          last_run: {
            status: "completed",
            started_at: minutesAgo(70),
            completed_at: minutesAgo(60),
          },
        },
        stale_sweep: {
          pending: 7,
          last_run: {
            status: "completed",
            started_at: minutesAgo(2),
            completed_at: minutesAgo(2),
            result: { stale: 3, refreshed: 2, failed: 1 },
          },
        },
        cache_refreshed_at: minutesAgo(5),
        lock_conflict: 'Embeddings were created with model "a", not "b".',
      }),
    });
    await render();

    const text = container.textContent ?? "";
    expect(text).toContain("Last run failed 10m ago");
    // A later catch-up shows beside the failed full run, not instead of it.
    expect(text).toContain("Last catch-up completed 3m ago");
    expect(text).toContain("recommendations reset required: embedding model changed");
    expect(text).toContain("Last run completed 1h ago");
    expect(text).toContain("No finished run recorded");
    expect(text).toContain("Newest cached row written 5m ago");
    expect(text).toContain("7 profiles wait for a rebuild");
    expect(text).toContain("Last stale sweep completed 2m ago");
    expect(container.querySelector('[role="alert"]')?.textContent).toContain(
      'Embeddings were created with model "a", not "b".',
    );
  });

  it("resets embeddings only after confirmation and reports the result", async () => {
    mocks.useRecommendationsStatus.mockReturnValue({ data: statusWith() });
    mocks.resetMutateAsync.mockResolvedValue({
      embeddings: 1200,
      taste_profiles: 3,
      taste_clusters: 9,
      cached_rows: 41,
    });
    await render();

    await click(findButton(container, "Reset embeddings"));
    expect(mocks.resetMutateAsync).not.toHaveBeenCalled();
    const dialog = document.body.querySelector('[role="alertdialog"]');
    expect(dialog?.textContent).toContain("Reset embeddings?");

    await click(findButton(dialog as HTMLElement, "Reset embeddings"));
    expect(mocks.resetMutateAsync).toHaveBeenCalledOnce();
    expect(mocks.toastSuccess).toHaveBeenCalledWith(
      expect.stringContaining("deleted 1,200 embeddings and cleared 3 taste profiles"),
    );
    expect(mocks.toastError).not.toHaveBeenCalled();
  });

  it.each([
    ["embeddings", true],
    ["taste_profiles", true],
    ["recommendations", true],
    // The reset stays available while only co-watch runs.
    ["cowatch", false],
  ] as const)("while the %s job runs, the reset's disabled state is %s", async (job, disabled) => {
    mocks.useRecommendationsStatus.mockReturnValue({
      data: statusWith({ [job]: { running: true, count: 1 } }),
    });
    await render();

    expect(findButton(container, "Reset embeddings")?.disabled).toBe(disabled);
  });
});
