// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Profile } from "@/api/types";

const mocks = vi.hoisted(() => ({
  useCreateProfile: vi.fn(),
  useUpdateProfile: vi.fn(),
  useUploadProfileAvatar: vi.fn(),
  useDeleteProfileAvatar: vi.fn(),
}));

vi.mock("@/hooks/queries/profiles", () => ({
  useCreateProfile: () => mocks.useCreateProfile(),
  useUpdateProfile: () => mocks.useUpdateProfile(),
  useUploadProfileAvatar: () => mocks.useUploadProfileAvatar(),
  useDeleteProfileAvatar: () => mocks.useDeleteProfileAvatar(),
}));

import { ProfileEditorDialog } from "./ProfileEditorDialog";

function makeProfile(overrides: Partial<Profile> = {}): Profile {
  return {
    id: "profile-1",
    name: "Main",
    avatar: "",
    has_pin: false,
    is_child: false,
    is_primary: false,
    max_content_rating: "",
    quality_preference: "auto",
    language: "en",
    subtitle_language: "",
    subtitle_mode: "auto",
    show_forced_subtitles: true,
    auto_skip_intro: false,
    auto_skip_credits: false,
    library_restrictions_enabled: false,
    allowed_library_ids: null,
    max_playback_quality: "",
    created_at: "2026-04-06T00:00:00Z",
    updated_at: "2026-04-06T00:00:00Z",
    ...overrides,
  };
}

describe("ProfileEditorDialog", () => {
  let container: HTMLDivElement;
  let root: Root;
  let originalResizeObserver: typeof globalThis.ResizeObserver | undefined;

  beforeEach(() => {
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);

    for (const hook of Object.values(mocks)) {
      hook.mockReset().mockReturnValue({ isPending: false, mutate: vi.fn(), mutateAsync: vi.fn() });
    }

    originalResizeObserver = globalThis.ResizeObserver;
    globalThis.ResizeObserver = class ResizeObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
    };
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
    if (originalResizeObserver) {
      globalThis.ResizeObserver = originalResizeObserver;
    } else {
      Reflect.deleteProperty(globalThis, "ResizeObserver");
    }
  });

  async function renderDialog(
    profile: Profile,
    support: { advisoryAgeSupported?: boolean; requireAdvisoryAgeSupported?: boolean } = {},
  ) {
    await act(async () => {
      root.render(
        <ProfileEditorDialog
          open
          profile={profile}
          libraries={[]}
          onOpenChange={() => {}}
          {...support}
        />,
      );
    });
  }

  it("offers the require-advisory-age switch once a limit is set", async () => {
    await renderDialog(makeProfile({ max_advisory_age: 10, require_advisory_age: true }), {
      advisoryAgeSupported: true,
      requireAdvisoryAgeSupported: true,
    });

    expect(document.body.textContent).toContain("Maximum advisory age");
    expect(document.body.textContent).toContain("Ages 10 and under");
    const toggle = document.querySelector<HTMLButtonElement>(
      'button[role="switch"][aria-describedby]',
    );
    expect(document.body.textContent).toContain("Hide titles without an advisory age");
    expect(toggle?.getAttribute("aria-checked")).toBe("true");
    // The lenient explanation would be wrong for a strict profile.
    expect(document.body.textContent).not.toContain(
      "Titles without an advisory age are limited by the content rating alone.",
    );
  });

  it("hides the require-advisory-age switch without a limit or support", async () => {
    await renderDialog(makeProfile(), {
      advisoryAgeSupported: true,
      requireAdvisoryAgeSupported: true,
    });
    expect(document.body.textContent).toContain("Edit profile");
    expect(document.body.textContent).toContain("Any content");
    expect(document.body.textContent).toContain("No limit");
    expect(document.body.textContent).not.toContain("Hide titles without an advisory age");

    await renderDialog(makeProfile({ id: "profile-2", max_advisory_age: 10 }), {
      advisoryAgeSupported: true,
    });
    expect(document.body.textContent).toContain("Maximum advisory age");
    expect(document.body.textContent).toContain("Ages 10 and under");
    expect(document.body.textContent).not.toContain("Hide titles without an advisory age");
  });

  it("hides the advisory-age control on a server without it", async () => {
    await renderDialog(makeProfile({ max_advisory_age: 10 }));

    expect(document.body.textContent).toContain("Maximum content rating");
    expect(document.body.textContent).not.toContain("Maximum advisory age");
  });
});
