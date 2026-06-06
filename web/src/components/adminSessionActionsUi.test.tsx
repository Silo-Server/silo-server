// @vitest-environment jsdom

import { act } from "react";
import type { ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminSession } from "@/api/types";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  api: (...args: unknown[]) => mocks.api(...args),
}));

vi.mock("sonner", () => ({
  toast: {
    error: (...args: unknown[]) => mocks.toastError(...args),
    success: (...args: unknown[]) => mocks.toastSuccess(...args),
  },
}));

vi.mock("@/components/ui/button", () => ({
  Button: ({
    children,
    className,
    disabled,
    onClick,
    "aria-label": ariaLabel,
  }: {
    children: ReactNode;
    className?: string;
    disabled?: boolean;
    onClick?: () => void;
    "aria-label"?: string;
  }) => (
    <button
      type="button"
      className={className}
      disabled={disabled}
      onClick={onClick}
      aria-label={ariaLabel}
    >
      {children}
    </button>
  ),
}));

vi.mock("@/components/ui/dropdown-menu", () => ({
  DropdownMenu: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DropdownMenuTrigger: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DropdownMenuContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DropdownMenuLabel: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DropdownMenuSeparator: () => <hr />,
  DropdownMenuItem: ({
    children,
    disabled,
    onSelect,
  }: {
    children: ReactNode;
    disabled?: boolean;
    onSelect?: () => void;
  }) => (
    <button type="button" disabled={disabled} onClick={() => onSelect?.()}>
      {children}
    </button>
  ),
}));

vi.mock("@/components/ui/dialog", () => ({
  Dialog: ({ children, open }: { children: ReactNode; open: boolean }) =>
    open ? <div>{children}</div> : null,
  DialogContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogDescription: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogFooter: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogHeader: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogTitle: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}));

import { AdminSessionActions } from "./AdminSessionActions";

const baseSession: AdminSession = {
  session_id: "session-1",
  user_id: 1,
  username: "alex",
  profile_id: "profile-1",
  media_file_id: 100,
  requested_media_file_id: 100,
  media_title: "Heat",
  media_type: "movie",
  play_method: "direct",
  reporting_node: "node-1",
  file_duration: 3600,
  started_at: "2026-03-24T12:00:00Z",
  updated_at: "2026-03-24T12:05:00Z",
  position_seconds: 300,
  is_paused: false,
  has_playback_control: true,
  audio_track_index: 0,
  transcode_audio: false,
  stream_bitrate_kbps: null,
  target_bitrate_kbps: null,
  source_audio_channels: null,
  source_bitrate_kbps: null,
};

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

describe("AdminSessionActions", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);

    mocks.api.mockReset();
    mocks.toastError.mockReset();
    mocks.toastSuccess.mockReset();
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
  });

  async function render(session: AdminSession) {
    await act(async () => {
      root.render(<AdminSessionActions session={session} />);
    });
  }

  it("shows limited copy and hides pause, resume, and message when playback control is unavailable", async () => {
    await render({
      ...baseSession,
      has_playback_control: false,
    });

    expect(container.textContent).toContain(
      "This session does not support live pause, resume, or messages. Stop and Terminate can still end playback.",
    );
    expect(findButton(container, "Pause")).toBeUndefined();
    expect(findButton(container, "Resume")).toBeUndefined();
    expect(findButton(container, "Message")).toBeUndefined();
    expect(findButton(container, "Stop")).toBeTruthy();
    expect(findButton(container, "Terminate")).toBeTruthy();
  });

  it("keeps the pause action in place and shows fallback copy when the backend schedules a fallback stop", async () => {
    mocks.api.mockResolvedValue({
      command_id: "cmd-1",
      status: "fallback_scheduled",
    });

    await render(baseSession);

    await click(findButton(container, "Pause"));

    expect(mocks.api).toHaveBeenCalledWith("/admin/sessions/session-1/pause", {
      method: "POST",
    });
    expect(mocks.toastSuccess).toHaveBeenCalledWith(
      "Pause could not reach the player directly. Silo will end the session shortly instead.",
    );
    expect(findButton(container, "Pause")).toBeTruthy();
    expect(findButton(container, "Resume")).toBeUndefined();
  });
});
