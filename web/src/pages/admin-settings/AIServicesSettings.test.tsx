import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import AIServicesSettings from "./AIServicesSettings";

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

const useSettingsFormMock = vi.fn((_options?: { keys: string[] }) => ({
  isLoading: false,
  getValue: (key: string) => values[key] ?? "",
  setValue: vi.fn(),
  dirtyCount: 0,
  dirtyKeys: [],
  isDirty: vi.fn(() => false),
  save: vi.fn(),
  discard: vi.fn(),
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
  useCheckAdminSettingsConnection: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

describe("AIServicesSettings", () => {
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
});
