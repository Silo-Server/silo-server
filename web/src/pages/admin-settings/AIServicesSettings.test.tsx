import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

import AIServicesSettings from "./AIServicesSettings";

const mocks = vi.hoisted(() => ({
  checkConnection: vi.fn(),
  discard: vi.fn(),
  save: vi.fn(),
  setValue: vi.fn(),
  toastError: vi.fn(),
}));

const values: Record<string, string> = {
  "ai.base_url": "https://text.example.test",
  "ai.chat_model": "chat-model",
  "ai.asr_base_url": "",
  "ai.asr_model": "whisper-model",
  "ai.max_concurrent_jobs": "2",
  "subtitle_ai.base_url": "https://legacy.example.test",
  "subtitle_ai.chat_model": "legacy-chat-model",
  "subtitle_ai.max_concurrent_jobs": "3",
  "subtitle_ai.enabled": "true",
  "subtitle_ai.transcribe_enabled": "false",
  "subtitle_ai.batch_size": "40",
  "subtitle_ai.context_neighbors": "2",
  "subtitle_ai.asr_chunk_seconds": "600",
  "subtitle_ai.transcribe_quota_jobs": "0",
  "subtitle_ai.transcribe_quota_period": "day",
  "metadata_ai.enabled": "false",
  "metadata_ai.on_view": "button",
};

let dirtyCount = 0;

const useSettingsFormMock = vi.fn((_options?: { keys: string[] }) => ({
  isLoading: false,
  getValue: (key: string) => values[key] ?? "",
  setValue: mocks.setValue,
  dirtyCount,
  dirtyKeys: [],
  isDirty: vi.fn(() => false),
  save: mocks.save,
  discard: mocks.discard,
  isSaving: false,
  restartRequired: false,
  sensitiveConfigured: ["subtitle_ai.api_key"],
  sensitiveManagedByEnv: [],
  buildConnectionCheckRequest: vi.fn(() => ({ values: {}, dirty_keys: [] })),
}));

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (options: { keys: string[] }) => useSettingsFormMock(options),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerSettings: () => ({ data: values }),
  useAdminSensitiveStatus: () => ({ data: { configured: ["ai.api_key"] } }),
  useUpdateServerSetting: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useCheckAdminSettingsConnection: () => ({
    mutateAsync: mocks.checkConnection,
    isPending: false,
  }),
}));

vi.mock("sonner", () => ({
  toast: {
    error: mocks.toastError,
  },
}));

describe("AIServicesSettings", () => {
  beforeEach(() => {
    dirtyCount = 0;
    mocks.checkConnection.mockReset();
    mocks.discard.mockReset();
    mocks.save.mockReset();
    mocks.setValue.mockReset();
    mocks.toastError.mockReset();
    values["ai.base_url"] = "https://text.example.test";
    values["ai.chat_model"] = "chat-model";
    values["ai.asr_base_url"] = "";
    values["ai.asr_model"] = "whisper-model";
    values["ai.max_concurrent_jobs"] = "2";
    values["subtitle_ai.batch_size"] = "40";
    values["subtitle_ai.context_neighbors"] = "2";
    values["subtitle_ai.asr_chunk_seconds"] = "600";
  });

  it("separates text translation from speech-to-text configuration", () => {
    const markup = renderToStaticMarkup(<AIServicesSettings />);

    expect(markup).toContain("Text translation");
    expect(markup).toContain("Speech-to-text");
    expect(markup).toContain("Test Text AI");
    expect(markup).toContain("Test Speech-to-Text");
    expect(markup).toContain("Uses the Text translation endpoint");
  });

  it("shows effective legacy endpoint values until modern keys are saved", () => {
    const currentBaseURL = values["ai.base_url"]!;
    const currentChatModel = values["ai.chat_model"]!;
    values["ai.base_url"] = "";
    values["ai.chat_model"] = "";

    try {
      const markup = renderToStaticMarkup(<AIServicesSettings />);

      expect(markup).toContain("https://legacy.example.test");
      expect(markup).toContain("legacy-chat-model");
    } finally {
      values["ai.base_url"] = currentBaseURL;
      values["ai.chat_model"] = currentChatModel;
    }
  });

  it("marks known chat-only fallback endpoints as incompatible with speech-to-text", () => {
    const currentBaseURL = values["ai.base_url"]!;
    values["ai.base_url"] = "https://openrouter.ai/api";

    try {
      const markup = renderToStaticMarkup(<AIServicesSettings />);

      expect(markup).toContain("Incompatible endpoint");
    } finally {
      values["ai.base_url"] = currentBaseURL;
    }
  });

  it("exposes transcription preset selection to assistive technology", () => {
    const markup = renderToStaticMarkup(<AIServicesSettings />);

    expect(markup).toContain('aria-pressed="false"');
  });

  it("explains feature dependencies and keeps advanced tuning secondary", () => {
    const markup = renderToStaticMarkup(<AIServicesSettings />);

    expect(markup).toContain("Text AI required");
    expect(markup).toContain("Speech-to-text required");
    expect(markup).toContain("Inactive until Description translation is enabled");
    expect(markup).toContain("Advanced");
  });

  it("points recommendation embeddings to their separate configuration", () => {
    const markup = renderToStaticMarkup(<AIServicesSettings />);

    expect(markup).toContain("Recommendation embeddings are configured separately");
    expect(markup).toContain('href="/admin/recommendations"');
    expect(markup).not.toContain("Changes take effect after a server restart");
  });

  it("applies a transcription preset", async () => {
    const user = userEvent.setup();
    render(<AIServicesSettings />);

    await user.click(screen.getByRole("button", { name: "Groq - fast" }));

    expect(mocks.setValue).toHaveBeenCalledWith("ai.asr_base_url", "https://api.groq.com/openai");
    expect(mocks.setValue).toHaveBeenCalledWith("ai.asr_model", "whisper-large-v3-turbo");
  });

  it("runs both connection checks and clears their results when drafts are discarded", async () => {
    const user = userEvent.setup();
    dirtyCount = 1;
    mocks.checkConnection
      .mockResolvedValueOnce({ success: true, message: "Text connection verified." })
      .mockResolvedValueOnce({ success: true, message: "Speech connection verified." });
    render(<AIServicesSettings />);

    await user.click(screen.getByRole("button", { name: "Test Text AI" }));
    await user.click(screen.getByRole("button", { name: "Test Speech-to-Text" }));
    expect(await screen.findByText("Text connection verified.")).toBeInTheDocument();
    expect(await screen.findByText("Speech connection verified.")).toBeInTheDocument();
    expect(mocks.checkConnection).toHaveBeenNthCalledWith(
      1,
      expect.objectContaining({ kind: "ai_chat" }),
    );
    expect(mocks.checkConnection).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({ kind: "ai_transcription" }),
    );

    await user.click(screen.getByRole("button", { name: "Discard" }));

    expect(mocks.discard).toHaveBeenCalledOnce();
    await waitFor(() => {
      expect(screen.queryByText("Text connection verified.")).not.toBeInTheDocument();
      expect(screen.queryByText("Speech connection verified.")).not.toBeInTheDocument();
    });
  });

  it("clears a prior connection result when its endpoint changes", async () => {
    const user = userEvent.setup();
    mocks.checkConnection.mockResolvedValue({
      success: true,
      message: "Text connection verified.",
    });
    render(<AIServicesSettings />);

    await user.click(screen.getByRole("button", { name: "Test Text AI" }));
    expect(await screen.findByText("Text connection verified.")).toBeInTheDocument();
    await user.clear(screen.getByRole("textbox", { name: "Base URL" }));

    expect(screen.queryByText("Text connection verified.")).not.toBeInTheDocument();
  });

  it.each([
    ["ai.max_concurrent_jobs", "1.5", "Max concurrent jobs must be a positive whole number."],
    ["subtitle_ai.batch_size", "2abc", "Subtitle batch size must be a positive whole number."],
    [
      "subtitle_ai.context_neighbors",
      "1.5",
      "Subtitle context lines must be zero or a positive whole number.",
    ],
    [
      "subtitle_ai.asr_chunk_seconds",
      "120seconds",
      "Transcription chunk length must be between 60 and 600 seconds.",
    ],
  ])("rejects malformed integer input for %s", async (key, malformedValue, message) => {
    const user = userEvent.setup();
    dirtyCount = 1;
    values[key] = malformedValue;
    render(<AIServicesSettings />);

    await user.click(screen.getByRole("button", { name: "Save Changes" }));

    expect(mocks.toastError).toHaveBeenCalledWith(message);
    expect(mocks.save).not.toHaveBeenCalled();
  });
});
