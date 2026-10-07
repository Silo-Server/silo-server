// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  useAdminServerSettings: vi.fn(),
  useAdminSensitiveStatus: vi.fn(),
  useRecommendationsStatus: vi.fn(),
  checkConnectionMutateAsync: vi.fn(),
  updateMutate: vi.fn(),
  updateMutateAsync: vi.fn(),
  triggerEmbeddingsMutate: vi.fn(),
  triggerTasteProfilesMutate: vi.fn(),
  triggerCowatchMutate: vi.fn(),
  triggerRecommendationsMutate: vi.fn(),
  resetMutateAsync: vi.fn(),
  resetPending: false,
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
    isPending: mocks.resetPending,
    mutateAsync: (...args: unknown[]) => mocks.resetMutateAsync(...args),
  }),
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

    mocks.useAdminServerSettings.mockReset();
    mocks.useAdminSensitiveStatus.mockReset();
    mocks.useRecommendationsStatus.mockReset();
    mocks.checkConnectionMutateAsync.mockReset();
    mocks.updateMutate.mockReset();
    mocks.updateMutateAsync.mockReset();
    mocks.triggerEmbeddingsMutate.mockReset();
    mocks.triggerTasteProfilesMutate.mockReset();
    mocks.triggerCowatchMutate.mockReset();
    mocks.triggerRecommendationsMutate.mockReset();
    mocks.resetMutateAsync.mockReset();
    mocks.resetPending = false;
    mocks.toastSuccess.mockReset();
    mocks.toastError.mockReset();

    mocks.useAdminServerSettings.mockReturnValue({
      data: {
        "recommendations.enabled": "true",
        "recommendations.embedding_base_url": "http://localhost:9999",
        "recommendations.embedding_model": "custom-model",
      },
      isLoading: false,
    });
    mocks.useAdminSensitiveStatus.mockReturnValue({
      data: { configured: ["recommendations.embedding_auth_token"] },
    });
    mocks.useRecommendationsStatus.mockReturnValue({ data: undefined });
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

  it("applies a provider preset to the embedding settings", async () => {
    mocks.updateMutateAsync.mockResolvedValueOnce({
      values: {
        "recommendations.embedding_base_url": "https://generativelanguage.googleapis.com/canonical",
        "recommendations.embedding_model": "canonical-gemini-model",
      },
      restart_required: true,
    });
    await render();

    await click(findButton(container, "Gemini"));

    expect(mocks.updateMutateAsync).toHaveBeenCalledOnce();
    expect(mocks.updateMutateAsync).toHaveBeenCalledWith({
      "recommendations.embedding_base_url": "https://generativelanguage.googleapis.com",
      "recommendations.embedding_model": "gemini-embedding-001",
    });

    const baseUrlInput = container.querySelector<HTMLInputElement>(
      'input[id="recommendations.embedding_base_url"]',
    );
    const modelInput = container.querySelector<HTMLInputElement>(
      'input[id="recommendations.embedding_model"]',
    );

    expect(baseUrlInput?.value).toBe("https://generativelanguage.googleapis.com/canonical");
    expect(modelInput?.value).toBe("canonical-gemini-model");
  });

  it("checks the current unsaved embedding draft", async () => {
    await render();

    const tokenInput = container.querySelector<HTMLInputElement>(
      'input[id="recommendations.embedding_auth_token"]',
    );
    expect(tokenInput).toBeTruthy();

    await changeInput(tokenInput, "draft-token");

    await click(findButton(container, "Check Connection"));

    expect(mocks.checkConnectionMutateAsync).toHaveBeenCalledWith({
      kind: "recommendations_embedding",
      body: {
        values: {
          "recommendations.enabled": "true",
          "recommendations.embedding_base_url": "http://localhost:9999",
          "recommendations.embedding_model": "custom-model",
          "recommendations.embedding_auth_token": "draft-token",
        },
        dirty_keys: ["recommendations.embedding_auth_token"],
      },
    });
  });

  it("shows each job's last run, the cache refresh time and a lock conflict", async () => {
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
        },
        recommendations: {
          ...idleJob(5),
          last_run: {
            status: "completed",
            started_at: minutesAgo(70),
            completed_at: minutesAgo(60),
          },
        },
        cache_refreshed_at: minutesAgo(5),
        lock_conflict: 'Embeddings were created with model "a", not "b".',
      }),
    });
    await render();

    const text = container.textContent ?? "";
    expect(text).toContain("Last run failed 10m ago");
    expect(text).toContain("recommendations reset required: embedding model changed");
    expect(text).toContain("Last run completed 1h ago");
    expect(text).toContain("No finished run recorded");
    expect(text).toContain("Cache refreshed 5m ago");
    expect(container.querySelector('[role="alert"]')?.textContent).toContain(
      'Embeddings were created with model "a", not "b".',
    );
  });

  it("shows no lock warning when the lock matches", async () => {
    mocks.useRecommendationsStatus.mockReturnValue({ data: statusWith() });
    await render();

    expect(container.querySelector('[role="alert"]')).toBeNull();
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

  it("does not reset when the confirmation is cancelled", async () => {
    mocks.useRecommendationsStatus.mockReturnValue({ data: statusWith() });
    await render();

    await click(findButton(container, "Reset embeddings"));
    const dialog = document.body.querySelector('[role="alertdialog"]');
    await click(findButton(dialog as HTMLElement, "Cancel"));

    expect(mocks.resetMutateAsync).not.toHaveBeenCalled();
    expect(document.body.querySelector('[role="alertdialog"]')).toBeNull();
  });

  it("reports a failed reset", async () => {
    mocks.useRecommendationsStatus.mockReturnValue({ data: statusWith() });
    mocks.resetMutateAsync.mockRejectedValue(
      new Error("Recommendation work that uses embeddings is running on this or another server."),
    );
    await render();

    await click(findButton(container, "Reset embeddings"));
    await click(
      findButton(
        document.body.querySelector('[role="alertdialog"]') as HTMLElement,
        "Reset embeddings",
      ),
    );

    expect(mocks.toastError).toHaveBeenCalledWith(
      "Recommendation work that uses embeddings is running on this or another server.",
    );
    expect(mocks.toastSuccess).not.toHaveBeenCalled();
  });

  it.each(["embeddings", "taste_profiles", "recommendations"] as const)(
    "disables the reset while the %s job runs",
    async (job) => {
      mocks.useRecommendationsStatus.mockReturnValue({
        data: statusWith({ [job]: { running: true, count: 1 } }),
      });
      await render();

      expect(findButton(container, "Reset embeddings")?.disabled).toBe(true);
    },
  );

  it("keeps the reset available while only co-watch runs", async () => {
    mocks.useRecommendationsStatus.mockReturnValue({
      data: statusWith({ cowatch: { running: true, count: 1 } }),
    });
    await render();

    expect(findButton(container, "Reset embeddings")?.disabled).toBe(false);
  });

  it("disables the reset while a reset is in flight", async () => {
    mocks.useRecommendationsStatus.mockReturnValue({ data: statusWith() });
    mocks.resetPending = true;
    await render();

    expect(findButton(container, "Reset embeddings")?.disabled).toBe(true);
  });
});
