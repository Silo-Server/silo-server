// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AdminSession } from "@/api/types";
import { sendAdminPlaybackCommand } from "@/api/v2/adminPlaybackCommands";
import { v2Fixture } from "@/api/v2/testing";
import { AdminSessionActions } from "./AdminSessionActions";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }));
vi.mock("@/api/v2/adminPlaybackCommands", () => ({
  captureAdminPlaybackCommandAuthority: () => ({}),
  sendAdminPlaybackCommand: vi.fn(),
  terminateAdminPlaybackSession: vi.fn(),
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const session = {
  session_id: "session-1",
  user_id: 7,
  username: "viewer",
  is_paused: false,
  has_playback_control: true,
} as AdminSession;

const sent = "Message sent";
const replayed =
  "Your earlier message request was already accepted. Send again to send a new message.";
const unconfirmed = "The player didn’t confirm the message. It may show up late or not at all.";

// The receipt decides what the admin is told and whether the dialog stays
// open. Only an acknowledged replay keeps the text for a deliberate resend;
// an unconfirmed one closes, so a second Send can't repeat the message.
describe("AdminSessionActions message receipt", () => {
  it.each([
    { outcome: "applied", delivery: "acknowledged", kind: "success", text: sent, open: false },
    { outcome: "applied", delivery: "dispatched", kind: "warning", text: unconfirmed, open: false },
    {
      outcome: "replayed",
      delivery: "dispatched",
      kind: "warning",
      text: unconfirmed,
      open: false,
    },
    { outcome: "replayed", delivery: "acknowledged", kind: "success", text: replayed, open: true },
  ] as const)("$outcome $delivery", async ({ outcome, delivery, kind, text, open }) => {
    vi.mocked(sendAdminPlaybackCommand).mockResolvedValue(
      v2Fixture<"POST /api/v2/admin/sessions/{session_id}/message">({
        command_id: "c1",
        sequence: 1,
        outcome,
        delivery,
      }),
    );
    const user = userEvent.setup();
    render(<AdminSessionActions session={session} />);
    await user.click(screen.getByRole("button", { name: "Session actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Message…" }));
    await user.type(await screen.findByRole("textbox"), "Back in five");
    await user.click(screen.getByRole("button", { name: "Send Message" }));

    await waitFor(() => expect(toast[kind]).toHaveBeenCalledWith(text));
    expect(toast[kind === "success" ? "warning" : "success"]).not.toHaveBeenCalled();
    if (open) {
      expect(screen.getByRole("textbox")).toHaveValue("Back in five");
    } else {
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    }
  });
});
