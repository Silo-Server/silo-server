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
}));

import AdminRecommendations from "./AdminRecommendations";

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
});
