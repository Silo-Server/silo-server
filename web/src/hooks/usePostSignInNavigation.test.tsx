import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { setProfileLaunchMode } from "@/lib/profileLaunch";
import { usePostSignInNavigation } from "./usePostSignInNavigation";

const mocks = vi.hoisted(() => ({
  navigate: vi.fn(),
  selectProfile: vi.fn(),
  listProfiles: vi.fn(),
}));

vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return { ...actual, useNavigate: () => mocks.navigate };
});

vi.mock("@/hooks/queries/profiles", () => ({
  listProfiles: (...args: unknown[]) => mocks.listProfiles(...args),
}));

vi.mock("@/hooks/useAuth", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/useAuth")>();
  return { ...actual, useAuth: () => ({ selectProfile: mocks.selectProfile }) };
});

const solo = { id: "solo", name: "Solo", has_pin: false };

describe("usePostSignInNavigation", () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
    mocks.navigate.mockReset();
    mocks.selectProfile.mockReset();
    mocks.listProfiles.mockReset().mockResolvedValue({ profiles: [solo] });
  });

  it("enters the sole unlocked profile when this browser remembers profiles", async () => {
    const { result } = renderHook(() => usePostSignInNavigation(null));

    await result.current({ password_change_required: false });

    expect(mocks.selectProfile).toHaveBeenCalledWith(solo);
    expect(mocks.navigate).toHaveBeenCalledWith("/");
  });

  it("shows the picker on a launch when this browser asks who is watching", async () => {
    // A tab opened after the switch keeps its own, still empty, profile choice.
    setProfileLaunchMode("ask");
    const { result } = renderHook(() => usePostSignInNavigation(null));

    await result.current({ password_change_required: false });

    expect(mocks.selectProfile).not.toHaveBeenCalled();
    expect(mocks.navigate).toHaveBeenCalledWith("/profiles");
  });
});
