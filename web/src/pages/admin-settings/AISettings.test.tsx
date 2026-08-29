import { render as renderDOM, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import AISettings from "./AISettings";

// The page links to Recommendations with a router <Link>, so it needs a router.
function render(ui: React.ReactElement) {
  return renderDOM(<MemoryRouter>{ui}</MemoryRouter>);
}

const mocks = vi.hoisted(() => ({
  checkConnection: vi.fn(),
  discard: vi.fn(),
  save: vi.fn(),
  setValue: vi.fn(),
  resetValue: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
}));

const values: Record<string, string> = {};

const DEFAULT_VALUES: Record<string, string> = {
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
let dirtyKeys: string[] = [];
const DEFAULT_SENSITIVE_CONFIGURED = ["subtitle_ai.api_key"];
let sensitiveConfigured: string[] = DEFAULT_SENSITIVE_CONFIGURED;

const useSettingsFormMock = vi.fn((_options?: { keys: string[] }) => ({
  isLoading: false,
  getValue: (key: string) => values[key] ?? "",
  setValue: mocks.setValue,
  resetValue: mocks.resetValue,
  dirtyCount,
  dirtyKeys,
  isDirty: (key: string) => dirtyKeys.includes(key),
  isClearStaged: (key: string) => dirtyKeys.includes(key) && (values[key] ?? "") === "",
  save: mocks.save,
  discard: mocks.discard,
  isSaving: false,
  restartRequired: false,
  sensitiveConfigured,
  sensitiveManagedByEnv: [],
  sensitiveStatusReady: true,
  sensitiveStatusError: false,
  buildConnectionCheckRequest: vi.fn(() => ({ values: {}, dirty_keys: [] })),
}));

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (options: { keys: string[] }) => useSettingsFormMock(options),
}));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => new Set(["ai.max_concurrent_jobs"]),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useCheckAdminSettingsConnection: () => ({
    mutateAsync: mocks.checkConnection,
    isPending: false,
  }),
}));

vi.mock("sonner", () => ({
  toast: {
    error: mocks.toastError,
    success: mocks.toastSuccess,
  },
}));

/** Opens a model tile's connect panel. */
async function openTile(user: ReturnType<typeof userEvent.setup>, name: string) {
  const tile = screen.getByRole("group", { name });
  await user.click(within(tile).getByRole("button", { name: /Connect|Manage/ }));
  return screen.getByRole("group", { name });
}

describe("AISettings", () => {
  beforeEach(() => {
    localStorage.clear();
    dirtyCount = 0;
    dirtyKeys = [];
    sensitiveConfigured = DEFAULT_SENSITIVE_CONFIGURED;
    for (const mock of Object.values(mocks)) mock.mockReset();
    for (const key of Object.keys(values)) delete values[key];
    Object.assign(values, DEFAULT_VALUES);
  });

  it("heads the page and groups models and features", () => {
    render(<AISettings />);

    expect(screen.getByRole("heading", { level: 1, name: "AI Services" })).toBeInTheDocument();
    expect(
      screen.queryByText(
        "Optional language models for subtitle translation, transcription, and descriptions.",
      ),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Models" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Features" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Text model" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Speech-to-text" })).toBeInTheDocument();
  });

  it("keeps model credentials behind the tile until it is expanded", async () => {
    const user = userEvent.setup();
    render(<AISettings />);

    expect(screen.queryByLabelText("Model")).not.toBeInTheDocument();

    const tile = await openTile(user, "Text model");
    expect(tile).toHaveAttribute("data-expanded", "true");
    expect(within(tile).getByLabelText("Base URL")).toBeInTheDocument();
    expect(within(tile).getByLabelText("Model")).toBeInTheDocument();

    await user.click(within(tile).getByRole("button", { name: "Close" }));
    expect(screen.queryByLabelText("Base URL")).not.toBeInTheDocument();
  });

  it("falls back to the legacy subtitle_ai values", async () => {
    const user = userEvent.setup();
    values["ai.base_url"] = "";
    values["ai.chat_model"] = "";

    render(<AISettings />);
    await openTile(user, "Text model");

    expect(screen.getByDisplayValue("https://legacy.example.test")).toBeInTheDocument();
    expect(screen.getByDisplayValue("legacy-chat-model")).toBeInTheDocument();
  });

  it("flags a chat-only endpoint as unable to transcribe", () => {
    values["ai.base_url"] = "https://openrouter.ai/api";

    render(<AISettings />);

    expect(screen.getByText("Cannot transcribe")).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Speech-to-text" })).toHaveAttribute(
      "data-state",
      "error",
    );
  });

  it("applies a speech-to-text preset", async () => {
    const user = userEvent.setup();
    render(<AISettings />);
    await openTile(user, "Speech-to-text");

    await user.click(screen.getByRole("button", { name: "Groq - fast" }));

    expect(mocks.setValue).toHaveBeenCalledWith("ai.asr_base_url", "https://api.groq.com/openai");
    expect(mocks.setValue).toHaveBeenCalledWith("ai.asr_model", "whisper-large-v3-turbo");
  });

  it("forces a tile open while it holds a staged change", () => {
    dirtyKeys = ["ai.chat_model"];
    dirtyCount = 1;

    render(<AISettings />);

    expect(screen.getByRole("group", { name: "Text model" })).toHaveAttribute(
      "data-expanded",
      "true",
    );
    expect(screen.getByLabelText("Model")).toBeInTheDocument();
  });

  it("leaves the speech tile closed when only the shared text endpoint is staged", () => {
    // The transcription check falls back to the text endpoint, so its keys are
    // part of that request — but they are edited in the text tile, not here.
    dirtyKeys = ["ai.base_url", "ai.api_key"];
    dirtyCount = 2;

    render(<AISettings />);

    expect(screen.getByRole("group", { name: "Text model" })).toHaveAttribute(
      "data-expanded",
      "true",
    );
    expect(screen.getByRole("group", { name: "Speech-to-text" })).not.toHaveAttribute(
      "data-expanded",
    );
  });

  it("gives the model panel actions a resting affordance instead of ghost text", async () => {
    const user = userEvent.setup();
    render(<AISettings />);

    const tile = await openTile(user, "Text model");
    expect(within(tile).getByRole("button", { name: "Test text model" })).toHaveAttribute(
      "data-variant",
      "secondary",
    );
    expect(within(tile).getByRole("button", { name: "Close" })).toHaveAttribute(
      "data-variant",
      "outline",
    );
  });

  it("says the features run on demand rather than on a schedule", () => {
    render(<AISettings />);

    expect(screen.getByText(/Nothing here runs on a schedule/)).toBeInTheDocument();
  });

  it("blocks turning on a feature whose model cannot serve it", () => {
    // A chat-only endpoint cannot transcribe, so speech-to-text is not ready.
    values["ai.base_url"] = "https://openrouter.ai/api";

    render(<AISettings />);

    expect(screen.getByRole("switch", { name: "Create subtitles from audio" })).toBeDisabled();
  });

  it("still lets an enabled feature be turned off after its model degrades", () => {
    values["ai.base_url"] = "https://openrouter.ai/api";
    values["subtitle_ai.transcribe_enabled"] = "true";

    render(<AISettings />);

    expect(screen.getByRole("switch", { name: "Create subtitles from audio" })).toBeEnabled();
  });

  it("says nothing under a feature whose model is ready", () => {
    render(<AISettings />);

    expect(screen.getByText("Translate subtitles")).toBeInTheDocument();
    expect(screen.queryByText("Needs the text model")).not.toBeInTheDocument();
    expect(screen.queryByText("Needs speech-to-text")).not.toBeInTheDocument();
  });

  it("names the missing model when a feature cannot run", () => {
    values["ai.base_url"] = "https://openrouter.ai/api";

    render(<AISettings />);

    // A chat-only endpoint cannot transcribe, so the speech feature is unmet.
    expect(screen.getByText("Needs speech-to-text")).toBeInTheDocument();
  });

  it("keeps AI tuning behind a collapsed advanced disclosure", async () => {
    const user = userEvent.setup();
    render(<AISettings />);

    const toggle = screen.getByRole("button", { name: /Advanced · 6 settings/ });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByLabelText("Jobs running at once")).not.toBeInTheDocument();

    await user.click(toggle);

    expect(screen.getByLabelText("Jobs running at once")).toBeInTheDocument();
    // Restart-only keys carry the badge instead of hint text.
    expect(screen.getAllByLabelText("Takes effect after a server restart").length).toBe(1);
  });

  it("separates server-wide tuning from the per-account limit", async () => {
    const user = userEvent.setup();
    render(<AISettings />);

    await user.click(screen.getByRole("button", { name: /Advanced · 6 settings/ }));

    expect(screen.getByRole("heading", { name: "Server-wide tuning" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Per-account limits" })).toBeInTheDocument();
    expect(
      screen.getByText("Counted per login account, shared by every profile on it."),
    ).toBeInTheDocument();
  });

  it("auto-expands the advanced section around a staged change", () => {
    dirtyKeys = ["subtitle_ai.batch_size"];
    dirtyCount = 1;

    render(<AISettings />);

    // A staged change auto-expands the section so the save bar cannot block on
    // a hidden field.
    expect(screen.getByRole("button", { name: /Advanced · 6 settings/ })).toBeInTheDocument();
    expect(screen.getByLabelText("Subtitle lines per request")).toBeInTheDocument();
  });

  it("offers Unlimited instead of a zero sentinel for the transcription allowance", async () => {
    const user = userEvent.setup();
    render(<AISettings />);

    await user.click(screen.getByRole("button", { name: /Advanced · 6 settings/ }));

    expect(screen.getByRole("checkbox", { name: "Unlimited" })).toBeChecked();
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
    // parseInt would stop at the first non-digit and let "5abc" through as 5,
    // saving a quota nobody typed.
    [
      "subtitle_ai.transcribe_quota_jobs",
      "5abc",
      "Transcription limit must be zero or a positive whole number.",
    ],
    [
      "subtitle_ai.transcribe_quota_jobs",
      "1.5",
      "Transcription limit must be zero or a positive whole number.",
    ],
  ])("rejects malformed integer input for %s", async (key, malformedValue, message) => {
    const user = userEvent.setup();
    dirtyCount = 1;
    values[key] = malformedValue;
    render(<AISettings />);

    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(mocks.toastError).toHaveBeenCalledWith(message);
    expect(mocks.save).not.toHaveBeenCalled();
  });

  it("runs the text model check against the staged values", async () => {
    const user = userEvent.setup();
    mocks.checkConnection.mockResolvedValue({
      success: true,
      message: "Text connection verified.",
    });
    render(<AISettings />);
    await openTile(user, "Text model");

    await user.click(screen.getByRole("button", { name: "Test text model" }));

    expect(await screen.findByText(/Text connection verified\./)).toBeInTheDocument();
    expect(mocks.checkConnection).toHaveBeenCalledWith(
      expect.objectContaining({ kind: "ai_chat" }),
    );
  });

  it("emptying a saved AI key reverts the draft instead of staging an empty value", async () => {
    const user = userEvent.setup();
    values["ai.api_key"] = "draft";
    render(<AISettings />);
    const tile = await openTile(user, "Text model");

    // No Replace step: the saved key is a masked, always-editable input.
    const input = within(tile).getByLabelText("API key");
    expect(input).toHaveAttribute("placeholder", "••••••••••••");
    await user.clear(input);

    // Staging "" would erase the stored key on the next save.
    expect(mocks.setValue).not.toHaveBeenCalledWith("ai.api_key", "");
    expect(mocks.resetValue).toHaveBeenCalledWith("ai.api_key");
  });

  it("clears the legacy key alongside the modern one", async () => {
    const user = userEvent.setup();
    render(<AISettings />);
    const tile = await openTile(user, "Text model");

    await user.click(within(tile).getByRole("button", { name: "Clear saved value" }));

    // An empty `ai.api_key` falls back to `subtitle_ai.api_key`, so clearing
    // only the modern key would leave the old secret in force.
    expect(mocks.setValue).toHaveBeenCalledWith("ai.api_key", "");
    expect(mocks.setValue).toHaveBeenCalledWith("subtitle_ai.api_key", "");
  });

  it("says what a staged clear of the speech key will do", async () => {
    const user = userEvent.setup();
    dirtyKeys = ["ai.asr_api_key"];
    dirtyCount = 1;
    values["ai.asr_api_key"] = "";
    sensitiveConfigured = ["ai.asr_api_key"];

    render(<AISettings />);
    const tile = screen.getByRole("group", { name: "Speech-to-text" });

    // A staged edit holds its tile open, so no expansion step is needed.
    expect(within(tile).getByLabelText("API key")).toHaveAttribute(
      "placeholder",
      "Will be cleared on save",
    );
    expect(
      within(tile).getByText("Save clears the stored value; type to set a new one instead."),
    ).toBeInTheDocument();
    await user.click(within(tile).getByRole("button", { name: "Keep saved value" }));
    expect(mocks.resetValue).toHaveBeenCalledWith("ai.asr_api_key");
  });
});
