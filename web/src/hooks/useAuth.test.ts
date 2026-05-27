import { describe, expect, it, vi } from "vitest";
import { ApiClientError } from "../api/client";
import type { Profile, User } from "../api/types";
import {
  endImpersonationWithRecovery,
  getBootstrapProfile,
  initializeAuthSession,
} from "./useAuth";

function makeProfile(overrides: Partial<Profile> = {}): Profile {
  return {
    id: "profile-1",
    name: "Alex",
    avatar: "",
    has_pin: false,
    is_child: false,
    is_primary: true,
    max_content_rating: "",
    quality_preference: "1080p",
    language: "en",
    subtitle_language: "",
    subtitle_mode: "auto",
    show_forced_subtitles: true,
    auto_skip_intro: false,
    auto_skip_credits: false,
    library_restrictions_enabled: false,
    allowed_library_ids: [],
    max_playback_quality: "",
    created_at: "2024-01-01T00:00:00Z",
    updated_at: "2024-01-01T00:00:00Z",
    ...overrides,
  };
}

describe("initializeAuthSession", () => {
  it("bootstraps the access token before fetching the current user", async () => {
    const bootstrapAccessToken = vi.fn<() => Promise<boolean>>().mockResolvedValue(true);
    const fetchCurrentUser = vi.fn<() => Promise<User>>().mockResolvedValue({
      id: 1,
      username: "admin",
      email: "admin@example.com",
      role: "admin",
      permissions: [],
      download_allowed: true,
      impersonation: null,
    });
    const applyCurrentUser = vi.fn();
    const restoreProfile = vi.fn();
    const recoverPreservedAdminSession = vi.fn<() => Promise<boolean>>().mockResolvedValue(false);
    const clearTokens = vi.fn();
    const clearActiveAuthState = vi.fn();

    await initializeAuthSession({
      refreshToken: "refresh-token",
      hasStoredImpersonationAdminSession: false,
      bootstrapAccessToken,
      fetchCurrentUser,
      applyCurrentUser,
      restoreProfile,
      recoverPreservedAdminSession,
      clearTokens,
      clearActiveAuthState,
    });

    expect(bootstrapAccessToken).toHaveBeenCalledTimes(1);
    expect(fetchCurrentUser).toHaveBeenCalledTimes(1);
    const bootstrapCallOrder = bootstrapAccessToken.mock.invocationCallOrder[0];
    const fetchUserCallOrder = fetchCurrentUser.mock.invocationCallOrder[0];

    expect(bootstrapCallOrder).toBeDefined();
    expect(fetchUserCallOrder).toBeDefined();
    expect(bootstrapCallOrder!).toBeLessThan(fetchUserCallOrder!);
  });

  it("clears stale tokens when bootstrap refresh fails", async () => {
    const bootstrapAccessToken = vi.fn<() => Promise<boolean>>().mockResolvedValue(false);
    const fetchCurrentUser = vi.fn<() => Promise<User>>();
    const applyCurrentUser = vi.fn();
    const restoreProfile = vi.fn();
    const recoverPreservedAdminSession = vi.fn<() => Promise<boolean>>().mockResolvedValue(false);
    const clearTokens = vi.fn();
    const clearActiveAuthState = vi.fn();

    await initializeAuthSession({
      refreshToken: "refresh-token",
      hasStoredImpersonationAdminSession: false,
      bootstrapAccessToken,
      fetchCurrentUser,
      applyCurrentUser,
      restoreProfile,
      recoverPreservedAdminSession,
      clearTokens,
      clearActiveAuthState,
    });

    expect(fetchCurrentUser).not.toHaveBeenCalled();
    expect(clearTokens).toHaveBeenCalledTimes(1);
  });

  it("recovers the preserved admin session when impersonated auth is stale during init", async () => {
    const bootstrapAccessToken = vi.fn<() => Promise<boolean>>().mockResolvedValue(true);
    const fetchCurrentUser = vi
      .fn<() => Promise<User>>()
      .mockRejectedValue(new ApiClientError(401, "unauthorized", "expired"));
    const applyCurrentUser = vi.fn();
    const restoreProfile = vi.fn();
    const recoverPreservedAdminSession = vi.fn<() => Promise<boolean>>().mockResolvedValue(true);
    const clearTokens = vi.fn();
    const clearActiveAuthState = vi.fn();

    await initializeAuthSession({
      refreshToken: "impersonated-refresh",
      hasStoredImpersonationAdminSession: true,
      bootstrapAccessToken,
      fetchCurrentUser,
      applyCurrentUser,
      restoreProfile,
      recoverPreservedAdminSession,
      clearTokens,
      clearActiveAuthState,
    });

    expect(fetchCurrentUser).toHaveBeenCalledTimes(1);
    expect(recoverPreservedAdminSession).toHaveBeenCalledTimes(1);
    expect(restoreProfile).toHaveBeenCalledTimes(1);
    expect(applyCurrentUser).not.toHaveBeenCalled();
    expect(clearTokens).not.toHaveBeenCalled();
    expect(clearActiveAuthState).not.toHaveBeenCalled();
  });

  it("clears stale impersonated tokens when admin recovery is not available", async () => {
    const bootstrapAccessToken = vi.fn<() => Promise<boolean>>().mockResolvedValue(true);
    const fetchCurrentUser = vi
      .fn<() => Promise<User>>()
      .mockRejectedValue(new ApiClientError(401, "unauthorized", "expired"));
    const applyCurrentUser = vi.fn();
    const restoreProfile = vi.fn();
    const recoverPreservedAdminSession = vi.fn<() => Promise<boolean>>().mockResolvedValue(false);
    const clearTokens = vi.fn();
    const clearActiveAuthState = vi.fn();

    await initializeAuthSession({
      refreshToken: "impersonated-refresh",
      hasStoredImpersonationAdminSession: true,
      bootstrapAccessToken,
      fetchCurrentUser,
      applyCurrentUser,
      restoreProfile,
      recoverPreservedAdminSession,
      clearTokens,
      clearActiveAuthState,
    });

    expect(recoverPreservedAdminSession).toHaveBeenCalledTimes(1);
    expect(clearTokens).toHaveBeenCalledTimes(1);
    expect(restoreProfile).not.toHaveBeenCalled();
    expect(clearActiveAuthState).not.toHaveBeenCalled();
  });
});

describe("getBootstrapProfile", () => {
  it("returns the only profile when it is not PIN protected", () => {
    expect(getBootstrapProfile([makeProfile()])?.id).toBe("profile-1");
  });

  it("returns null when multiple profiles exist", () => {
    expect(getBootstrapProfile([makeProfile(), makeProfile({ id: "profile-2" })])).toBeNull();
  });

  it("returns null when the only profile is PIN protected", () => {
    expect(getBootstrapProfile([makeProfile({ has_pin: true })])).toBeNull();
  });
});

describe("endImpersonationWithRecovery", () => {
  it("restores the preserved admin session when ending impersonation fails with stale auth", async () => {
    const endImpersonationRequest = vi
      .fn<() => Promise<void>>()
      .mockRejectedValue(new ApiClientError(401, "unauthorized", "expired"));
    const loadStoredImpersonationAdminSession = vi.fn().mockReturnValue({
      accessToken: "admin-access",
      refreshToken: "admin-refresh",
      returnPath: "/admin/users/42",
    });
    const restoreAdminUser = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);
    const clearAuthState = vi.fn();
    const clearActiveAuthState = vi.fn();

    await endImpersonationWithRecovery({
      endImpersonationRequest,
      loadStoredImpersonationAdminSession,
      restoreAdminUser,
      clearAuthState,
      clearActiveAuthState,
    });

    expect(endImpersonationRequest).toHaveBeenCalledTimes(1);
    expect(loadStoredImpersonationAdminSession).toHaveBeenCalledTimes(1);
    expect(restoreAdminUser).toHaveBeenCalledWith({
      accessToken: "admin-access",
      refreshToken: "admin-refresh",
      returnPath: "/admin/users/42",
    });
    expect(clearAuthState).not.toHaveBeenCalled();
    expect(clearActiveAuthState).not.toHaveBeenCalled();
  });

  it("restores the preserved admin session when the server no longer considers the session impersonating", async () => {
    const endImpersonationRequest = vi
      .fn<() => Promise<void>>()
      .mockRejectedValue(new ApiClientError(400, "not_impersonating", "already ended"));
    const loadStoredImpersonationAdminSession = vi.fn().mockReturnValue({
      accessToken: "admin-access",
      refreshToken: "admin-refresh",
      returnPath: "/admin/users/42",
    });
    const restoreAdminUser = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);
    const clearAuthState = vi.fn();
    const clearActiveAuthState = vi.fn();

    await endImpersonationWithRecovery({
      endImpersonationRequest,
      loadStoredImpersonationAdminSession,
      restoreAdminUser,
      clearAuthState,
      clearActiveAuthState,
    });

    expect(restoreAdminUser).toHaveBeenCalledWith({
      accessToken: "admin-access",
      refreshToken: "admin-refresh",
      returnPath: "/admin/users/42",
    });
    expect(clearAuthState).not.toHaveBeenCalled();
    expect(clearActiveAuthState).not.toHaveBeenCalled();
  });

  it("keeps non-auth failures surfaced instead of restoring the admin session", async () => {
    const error = new ApiClientError(500, "server_error", "boom");
    const endImpersonationRequest = vi.fn<() => Promise<void>>().mockRejectedValue(error);
    const loadStoredImpersonationAdminSession = vi.fn().mockReturnValue({
      accessToken: "admin-access",
      refreshToken: "admin-refresh",
      returnPath: "/admin/users/42",
    });
    const restoreAdminUser = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);
    const clearAuthState = vi.fn();
    const clearActiveAuthState = vi.fn();

    await expect(
      endImpersonationWithRecovery({
        endImpersonationRequest,
        loadStoredImpersonationAdminSession,
        restoreAdminUser,
        clearAuthState,
        clearActiveAuthState,
      }),
    ).rejects.toBe(error);

    expect(restoreAdminUser).not.toHaveBeenCalled();
    expect(clearAuthState).not.toHaveBeenCalled();
    expect(clearActiveAuthState).not.toHaveBeenCalled();
  });

  it("clears auth when ending impersonation succeeds without a preserved admin session", async () => {
    const endImpersonationRequest = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);
    const loadStoredImpersonationAdminSession = vi.fn().mockReturnValue(null);
    const restoreAdminUser = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);
    const clearAuthState = vi.fn();
    const clearActiveAuthState = vi.fn();

    await endImpersonationWithRecovery({
      endImpersonationRequest,
      loadStoredImpersonationAdminSession,
      restoreAdminUser,
      clearAuthState,
      clearActiveAuthState,
    });

    expect(clearAuthState).toHaveBeenCalledTimes(1);
    expect(restoreAdminUser).not.toHaveBeenCalled();
    expect(clearActiveAuthState).not.toHaveBeenCalled();
  });

  it("restores the preserved admin session after a successful end request", async () => {
    const endImpersonationRequest = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);
    const loadStoredImpersonationAdminSession = vi.fn().mockReturnValue({
      accessToken: "admin-access",
      refreshToken: "admin-refresh",
      returnPath: "/admin/users/42",
    });
    const restoreAdminUser = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);
    const clearAuthState = vi.fn();
    const clearActiveAuthState = vi.fn();

    await endImpersonationWithRecovery({
      endImpersonationRequest,
      loadStoredImpersonationAdminSession,
      restoreAdminUser,
      clearAuthState,
      clearActiveAuthState,
    });

    expect(restoreAdminUser).toHaveBeenCalledWith({
      accessToken: "admin-access",
      refreshToken: "admin-refresh",
      returnPath: "/admin/users/42",
    });
    expect(clearAuthState).not.toHaveBeenCalled();
    expect(clearActiveAuthState).not.toHaveBeenCalled();
  });
});
