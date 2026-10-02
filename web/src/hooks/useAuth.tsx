import { createContext, useCallback, useContext, useEffect, useRef, useState } from "react";
import { endSessionWithProvider } from "@/api/v2/providerLogout";
import { clearSignedOut, markSignedOut } from "@/lib/externalSignIn";
import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  ApiClientError,
  bootstrapAccessToken,
  captureSessionIdentity,
  getAccessToken,
  isSessionIdentityCurrent,
  lastRefreshFailureWasProviderOutage,
  lastRefreshFailureWasTransient,
  onProfileUnverified,
  onRoleChanged,
  onSessionRejected,
  refreshAuthentication,
  setAccessToken,
  setProfileId,
  setProfileToken,
  setRefreshToken,
  type SessionIdentitySnapshot,
} from "@/api/client";
import { storage } from "@/utils/storage";
import type { LoginResponse, Profile, User } from "@/api/types";
import { v2, V2ProblemError, type V2Result } from "@/api/v2/request";
import { listProfiles, verifyProfilePIN, type ProfileVerification } from "@/hooks/queries/profiles";
import { restoreUserSession, sessionFromTokenPair, userFromAccount } from "@/api/v2/account";
import { queryClient } from "@/lib/query-client";
import { authProviderQueryOptions } from "@/hooks/queries/authProviders";
import {
  clearStoredImpersonationAdminSession,
  loadStoredImpersonationAdminSession,
  saveStoredImpersonationAdminSession,
  type StoredImpersonationAdminSession,
} from "@/lib/impersonationSession";

/** One sign-in option the server offers, as the v2 listAuthProviders operation describes it. */
export type AuthProviderOption = V2Result<"GET /api/v2/auth/providers">["items"][number];

interface AuthState {
  user: User | null;
  /** The signed-in account while it holds a temporary password; `user` is null until it is changed. */
  pendingPasswordChange: User | null;
  profile: Profile | null;
  loading: boolean;
  setupLoading: boolean;
  setupRequired: boolean;
  /** The first-run wizard was finished on this server; /setup must not reopen. */
  setupCompleted: boolean;
  /** Re-reads the public setup status, e.g. after the wizard records completion. */
  refreshSetupStatus: () => Promise<void>;
  providers: AuthProviderOption[];
  /** Re-reads the server's current sign-in providers and local-password policy. */
  refreshSignInProviders: () => Promise<void>;
  /**
   * The stored session could not be restored because the server or its
   * sign-in provider could not be reached (not because it was refused). The
   * session is kept; retrySessionRestore tries again.
   */
  sessionRestoreUnavailable: boolean;
  /**
   * With sessionRestoreUnavailable: the server answered 503
   * provider_unavailable, so it is the sign-in provider that could not be
   * reached rather than the server.
   */
  sessionRestoreProviderUnavailable: boolean;
  retrySessionRestore: () => void;
  isImpersonating: boolean;
  /** Resolves with the signed-in account; a temporary password confines it to changing the password. */
  login: (username: string, password: string, provider?: string) => Promise<User>;
  /** Swaps a temporary-password session for an unrestricted one after the password was changed. */
  settleTemporaryPassword: () => Promise<void>;
  /** Re-reads the signed-in account, e.g. after an admin changed its permissions. */
  refreshAccount: () => Promise<void>;
  completeLogin: (data: LoginResponse) => void;
  setupInitialUser: (username: string, email: string, password: string) => Promise<void>;
  signup: (username: string, email: string, password: string, inviteCode: string) => Promise<void>;
  beginImpersonation: (data: LoginResponse, returnPath: string) => void;
  endImpersonation: () => Promise<void>;
  /** Signs out of Silo and, when the sign-in provider offers it, out of the provider too. */
  logout: () => void;
  /**
   * Signs out of Silo only, leaving the sign-in provider's own session open,
   * for "Not you? Switch account" before signing in as someone else.
   */
  logoutOfSiloOnly: () => void;
  selectProfile: (profile: Profile, profileToken?: string) => void;
  verifyProfilePin: (profileId: string, pin: string) => Promise<ProfileVerification>;
  clearProfile: () => void;
}

const AuthContext = createContext<AuthState | null>(null);

export function getBootstrapProfile(profiles: Profile[]): Profile | null {
  if (profiles.length !== 1) {
    return null;
  }
  const profile = profiles[0];
  if (!profile) {
    return null;
  }
  return profile.has_pin ? null : profile;
}

function isRecoverableImpersonationAuthError(error: unknown): boolean {
  // The current-user fetch and endImpersonation run over v2 and fail with a
  // Problem: a stale session is 401, and a session the server no longer
  // considers impersonating is 409 `conflict`. The ApiClientError branch keeps
  // the v1 admin impersonate flow's answers recoverable.
  if (error instanceof V2ProblemError) {
    return error.status === 401 || (error.status === 409 && error.problemType === "conflict");
  }

  if (!(error instanceof ApiClientError)) {
    return false;
  }

  if (error.status === 401) {
    return true;
  }

  return error.status === 400 && error.code === "not_impersonating";
}

/**
 * Whether a failed account read is the server refusing the session (a 4xx
 * answer) rather than failing to answer at all (a network error, a timeout,
 * a 5xx, a gateway page). Only a refusal may end a stored session.
 */
function isSessionRefusal(error: unknown): boolean {
  const status =
    error instanceof V2ProblemError || error instanceof ApiClientError ? error.status : null;
  return status !== null && status >= 400 && status < 500 && status !== 408 && status !== 429;
}

export async function initializeAuthSession<TUser>({
  refreshToken,
  hasStoredImpersonationAdminSession,
  bootstrapAccessToken,
  fetchCurrentUser,
  applyCurrentUser,
  restoreProfile,
  recoverPreservedAdminSession,
  clearTokens,
  clearActiveAuthState,
  markRestoreUnavailable = () => {},
}: {
  refreshToken: string | null;
  hasStoredImpersonationAdminSession: boolean;
  /**
   * Exchanges the stored refresh token: true when restored, false when the
   * server refused the session, "unavailable" when it could not answer
   * (5xx, such as 503 provider_unavailable, or no network).
   */
  bootstrapAccessToken: () => Promise<boolean | "unavailable">;
  fetchCurrentUser: () => Promise<TUser>;
  applyCurrentUser: (user: TUser) => void;
  restoreProfile: () => void;
  recoverPreservedAdminSession: () => Promise<boolean>;
  clearTokens: () => void;
  clearActiveAuthState: () => void;
  /** Keeps the stored session for a retry after an outage. */
  markRestoreUnavailable?: () => void;
}): Promise<void> {
  if (!refreshToken) {
    try {
      const recovered = await recoverPreservedAdminSession();
      if (recovered) {
        restoreProfile();
      }
    } catch {
      clearActiveAuthState();
    }
    return;
  }

  const bootstrapped = await bootstrapAccessToken();
  if (bootstrapped === "unavailable") {
    // The server said nothing about the session (a fail_closed provider
    // outage keeps it valid), so the refresh token stays for a retry.
    markRestoreUnavailable();
    return;
  }
  if (!bootstrapped) {
    if (hasStoredImpersonationAdminSession) {
      try {
        const recovered = await recoverPreservedAdminSession();
        if (recovered) {
          restoreProfile();
          return;
        }
      } catch {
        clearActiveAuthState();
        return;
      }
    }

    clearTokens();
    return;
  }

  try {
    const currentUser = await fetchCurrentUser();
    applyCurrentUser(currentUser);
    restoreProfile();
  } catch (error) {
    if (!isSessionRefusal(error)) {
      // The session was restored; only reading the account failed (a
      // timeout, the network, a 5xx). Keep it for a retry.
      markRestoreUnavailable();
      return;
    }
    if (hasStoredImpersonationAdminSession && isRecoverableImpersonationAuthError(error)) {
      try {
        const recovered = await recoverPreservedAdminSession();
        if (recovered) {
          restoreProfile();
          return;
        }
      } catch {
        clearActiveAuthState();
        return;
      }
    }

    clearTokens();
  }
}

export async function endImpersonationWithRecovery({
  endImpersonationRequest,
  loadStoredImpersonationAdminSession,
  restoreAdminUser,
  clearAuthState,
  clearActiveAuthState,
}: {
  endImpersonationRequest: () => Promise<void>;
  loadStoredImpersonationAdminSession: () => StoredImpersonationAdminSession | null;
  restoreAdminUser: (storedSession: StoredImpersonationAdminSession) => Promise<void>;
  clearAuthState: () => void;
  clearActiveAuthState: () => void;
}): Promise<void> {
  const restorePreservedAdminSession = async (storedSession: StoredImpersonationAdminSession) => {
    try {
      await restoreAdminUser(storedSession);
    } catch (error) {
      clearActiveAuthState();
      throw error;
    }
  };

  try {
    await endImpersonationRequest();
  } catch (error) {
    const storedSession = loadStoredImpersonationAdminSession();
    if (storedSession && isRecoverableImpersonationAuthError(error)) {
      await restorePreservedAdminSession(storedSession);
      return;
    }
    throw error;
  }

  const storedSession = loadStoredImpersonationAdminSession();
  if (!storedSession) {
    clearAuthState();
    return;
  }

  await restorePreservedAdminSession(storedSession);
}

export function AuthProvider({ children }: { children: ReactNode }) {
  // The account the session authenticates. A temporary password confines the
  // session to choosing a new one, so until then the app treats it as signed
  // out: nothing keyed on `user` runs, and only the password change sees it.
  const [account, setUser] = useState<User | null>(null);
  const user = account?.password_change_required ? null : account;
  const pendingPasswordChange = account?.password_change_required ? account : null;
  const [profile, setProfile] = useState<Profile | null>(null);
  const [loading, setLoading] = useState(true);
  const [setupLoading, setSetupLoading] = useState(true);
  const [setupRequired, setSetupRequired] = useState(false);
  const [setupCompleted, setSetupCompleted] = useState(false);
  // Public discovery is fetched explicitly at boot and when a sign-in surface
  // needs it. Clearing account caches must not start another public read.
  const providerQuery = useQuery({ ...authProviderQueryOptions(), enabled: false }, queryClient);
  const providers = providerQuery.data?.items ?? [];
  const refreshSignInProviders = useCallback(async () => {
    try {
      await queryClient.fetchQuery(authProviderQueryOptions());
    } catch {
      // Keep the last known discovery until the next visit or provider write.
    }
  }, []);
  const [sessionRestoreUnavailable, setSessionRestoreUnavailable] = useState(false);
  const [sessionRestoreProviderUnavailable, setSessionRestoreProviderUnavailable] = useState(false);
  const [restoreAttempt, setRestoreAttempt] = useState(0);
  const isImpersonating = Boolean(user?.impersonation?.active);
  const soleProfileBootstrapRef = useRef<string | null>(null);
  // The committed account, for the auth callbacks, which run after commit.
  const signedInUserIdRef = useRef<number | null>(null);
  useEffect(() => {
    signedInUserIdRef.current = account?.id ?? null;
  }, [account]);

  const restoreProfile = useCallback(() => {
    const savedProfile = storage.get(storage.KEYS.CURRENT_PROFILE);
    if (!savedProfile) {
      return;
    }
    try {
      const restoredProfile = JSON.parse(savedProfile) as Profile;
      if (restoredProfile.id) {
        setProfileId(restoredProfile.id);
      }
      setProfile(restoredProfile);
    } catch {
      // invalid JSON, ignore
    }
  }, []);

  const clearProfile = useCallback(() => {
    setProfileId(null);
    setProfileToken(null);
    storage.remove(storage.KEYS.CURRENT_PROFILE);
    setProfile(null);
  }, []);

  const applyAuthenticatedUser = useCallback(
    (
      data: LoginResponse,
      options: {
        preserveStoredImpersonationAdminSession?: boolean;
      } = {},
    ) => {
      // A different account replacing a signed-in one. Drop the old account's
      // cache before the new one renders, so the new account's reads start on
      // an empty cache instead of being thrown away a render later.
      if (signedInUserIdRef.current !== null && signedInUserIdRef.current !== data.user.id) {
        queryClient.clear();
      }
      setAccessToken(data.access_token);
      setRefreshToken(data.refresh_token);
      setSessionRestoreUnavailable(false);
      clearSignedOut();
      if (!options.preserveStoredImpersonationAdminSession) {
        clearStoredImpersonationAdminSession();
      }
      clearProfile();
      setUser(data.user);
      setSetupRequired(false);
    },
    [clearProfile],
  );

  const clearActiveAuthState = useCallback(() => {
    setAccessToken(null);
    setRefreshToken(null);
    setSessionRestoreUnavailable(false);
    clearProfile();
    queryClient.clear();
    setUser(null);
    setSetupRequired(false);
  }, [clearProfile]);

  const clearAuthState = useCallback(() => {
    clearActiveAuthState();
    clearStoredImpersonationAdminSession();
  }, [clearActiveAuthState]);

  const restoreAdminUser = useCallback(
    async (
      storedSession: { accessToken: string; refreshToken: string },
      isCurrent: () => boolean = () => true,
    ) => {
      const restoredSession = await restoreUserSession(storedSession);
      // A sign-in that replaced the session during the exchange keeps it.
      if (!isCurrent()) return false;
      clearProfile();
      queryClient.clear();
      setAccessToken(restoredSession.accessToken);
      setRefreshToken(restoredSession.refreshToken);
      clearStoredImpersonationAdminSession();
      setUser(restoredSession.user);
      setSetupRequired(false);
      return true;
    },
    [clearProfile],
  );

  const recoverPreservedAdminSession = useCallback(
    async (isCurrent?: () => boolean) => {
      const storedSession = loadStoredImpersonationAdminSession();
      if (!storedSession) {
        return false;
      }

      return restoreAdminUser(storedSession, isCurrent);
    },
    [restoreAdminUser],
  );

  const beginImpersonation = useCallback(
    (data: LoginResponse, returnPath: string) => {
      const accessToken = getAccessToken();
      const refreshToken = storage.get(storage.KEYS.REFRESH_TOKEN);

      if (accessToken && refreshToken) {
        saveStoredImpersonationAdminSession({
          accessToken,
          refreshToken,
          returnPath,
        });
      } else {
        clearStoredImpersonationAdminSession();
      }

      queryClient.clear();
      applyAuthenticatedUser(data, {
        preserveStoredImpersonationAdminSession: true,
      });
    },
    [applyAuthenticatedUser],
  );

  const endImpersonation = useCallback(async () => {
    await endImpersonationWithRecovery({
      endImpersonationRequest: () => v2("POST /api/v2/auth/impersonation/end"),
      loadStoredImpersonationAdminSession,
      restoreAdminUser: async (storedSession) => {
        await restoreAdminUser(storedSession);
      },
      clearAuthState,
      clearActiveAuthState,
    });
  }, [clearActiveAuthState, clearAuthState, restoreAdminUser]);

  const refreshSetupStatus = useCallback(async () => {
    try {
      const status = await v2("GET /api/v2/system/setup");
      setSetupRequired(status.needs_setup);
      setSetupCompleted(status.wizard_completed === true);
    } catch {
      // Keep the last known status; the next app load re-reads it.
    }
  }, []);

  const endSession = useCallback(
    (withProvider: boolean) => {
      // Fire and forget the server logout, and the sign-in provider's own
      // logout when asked and it offers one; the bearer is captured before
      // the state clears. The mark keeps /login from sending this tab
      // straight back to the provider.
      const accessToken = getAccessToken();
      markSignedOut();
      if (accessToken) {
        void endSessionWithProvider(accessToken, undefined, { withProvider });
      }
      clearAuthState();
      void refreshSignInProviders();
    },
    [clearAuthState, refreshSignInProviders],
  );
  // An administrator viewing as someone leaves the account's provider
  // session alone: the server answers no provider sign-out for it anyway.
  const logout = useCallback(() => endSession(!isImpersonating), [endSession, isImpersonating]);
  const logoutOfSiloOnly = useCallback(() => endSession(false), [endSession]);

  const verifyProfilePin = useCallback(
    async (profileId: string, pin: string): Promise<ProfileVerification> => {
      return verifyProfilePIN(profileId, pin);
    },
    [],
  );

  const selectProfile = useCallback(
    (p: Profile, profileToken?: string) => {
      const profileChanged = profile?.id !== p.id;

      setProfileId(p.id);
      setProfileToken(profileToken ?? null);
      if (profileChanged) {
        queryClient.clear();
      }
      storage.set(storage.KEYS.CURRENT_PROFILE, JSON.stringify(p));
      setProfile(p);
    },
    [profile?.id],
  );

  useEffect(() => {
    onProfileUnverified(clearProfile);
    return () => onProfileUnverified(null);
  }, [clearProfile]);

  // The server stopped accepting this session mid-use. An admin viewing as
  // another user goes back to their own preserved session, as the boot
  // restore does; otherwise the session and its cached pages are dropped so
  // RequireAuth sends the user to sign-in. Requests refused on the same
  // session while a recovery runs join it instead of spending the admin's
  // refresh token again. Nothing here overrides a sign-in that replaced the
  // rejected session meanwhile.
  const sessionRejectionRef = useRef<{
    session: SessionIdentitySnapshot;
    handling: Promise<void>;
  } | null>(null);
  useEffect(() => {
    onSessionRejected(() => {
      const inFlight = sessionRejectionRef.current;
      if (inFlight && isSessionIdentityCurrent(inFlight.session)) return;
      const session = captureSessionIdentity();
      const isCurrent = () => isSessionIdentityCurrent(session);
      const handling = (async () => {
        try {
          if (await recoverPreservedAdminSession(isCurrent)) {
            restoreProfile();
            return;
          }
        } catch {
          // The admin session is gone too; fall through to sign-in.
        }
        if (isCurrent()) clearActiveAuthState();
      })().finally(() => {
        if (sessionRejectionRef.current?.handling === handling) sessionRejectionRef.current = null;
      });
      sessionRejectionRef.current = { session, handling };
    });
    return () => onSessionRejected(null);
  }, [clearActiveAuthState, recoverPreservedAdminSession, restoreProfile]);

  useEffect(() => {
    let cancelled = false;

    async function loadSetupStatus() {
      try {
        // Independent reads: a failed provider list must not blank the setup
        // status, or an admin visiting /setup during that outage would see
        // the finished wizard again.
        const [status] = await Promise.allSettled([
          v2("GET /api/v2/system/setup"),
          queryClient.fetchQuery(authProviderQueryOptions()),
        ]);
        if (cancelled) {
          return;
        }
        if (status.status === "fulfilled") {
          setSetupRequired(status.value.needs_setup);
          setSetupCompleted(status.value.wizard_completed === true);
        } else {
          setSetupRequired(false);
          setSetupCompleted(false);
        }
      } finally {
        if (!cancelled) {
          setSetupLoading(false);
        }
      }
    }

    async function restoreSession() {
      // A sign-in on another path (the OAuth completion page, a login,
      // impersonation) or a sign-out can replace the session while this
      // restore waits on the network. From then on the restore speaks for a
      // session that is gone: it must not apply its user or clear the new
      // session's tokens.
      let session = captureSessionIdentity();
      const superseded = () => cancelled || !isSessionIdentityCurrent(session);
      let providerOutage = false;
      try {
        await initializeAuthSession({
          refreshToken: storage.get(storage.KEYS.REFRESH_TOKEN),
          hasStoredImpersonationAdminSession: Boolean(loadStoredImpersonationAdminSession()),
          bootstrapAccessToken: async () => {
            const restored = await bootstrapAccessToken();
            // Installing the restored token starts the session the rest of
            // the restore answers for. The refresh single-flight already
            // discards an exchange that another sign-in overtook.
            if (restored) session = captureSessionIdentity();
            if (!restored && lastRefreshFailureWasTransient()) {
              providerOutage = lastRefreshFailureWasProviderOutage();
              return "unavailable";
            }
            return restored;
          },
          markRestoreUnavailable: () => {
            if (superseded()) {
              return;
            }
            setSessionRestoreUnavailable(true);
            setSessionRestoreProviderUnavailable(providerOutage);
          },
          fetchCurrentUser: () => v2("GET /api/v2/account/me").then(userFromAccount),
          applyCurrentUser: (currentUser) => {
            if (superseded()) {
              return;
            }
            setUser(currentUser);
          },
          restoreProfile: () => {
            if (superseded()) {
              return;
            }
            restoreProfile();
          },
          recoverPreservedAdminSession: async () => {
            if (superseded()) {
              return false;
            }
            return recoverPreservedAdminSession();
          },
          clearTokens: () => {
            if (superseded()) {
              return;
            }
            setAccessToken(null);
            setRefreshToken(null);
          },
          clearActiveAuthState: () => {
            if (superseded()) {
              return;
            }
            clearActiveAuthState();
          },
        });
      } finally {
        if (!cancelled) {
          setLoading(false);
        }
      }
    }

    // The restore runs beside the public setup reads, not after them. Those
    // reads are sent first because a request issued while the restore is in
    // flight waits for it, and they need no session.
    void loadSetupStatus();
    void restoreSession();

    return () => {
      cancelled = true;
    };
  }, [clearActiveAuthState, recoverPreservedAdminSession, restoreProfile, restoreAttempt]);

  const retrySessionRestore = useCallback(() => {
    setSessionRestoreUnavailable(false);
    setLoading(true);
    setRestoreAttempt((attempt) => attempt + 1);
  }, []);

  useEffect(() => {
    if (!user) {
      soleProfileBootstrapRef.current = null;
      return;
    }

    if (profile || storage.get(storage.KEYS.PROFILE_ID)) {
      return;
    }

    const bootstrapKey = `${user.id}:${user.impersonation?.impersonator_user_id ?? 0}`;
    if (soleProfileBootstrapRef.current === bootstrapKey) {
      return;
    }
    soleProfileBootstrapRef.current = bootstrapKey;

    let cancelled = false;
    listProfiles()
      .then((data) => {
        if (cancelled) {
          return;
        }
        const soleProfile = getBootstrapProfile(data.profiles ?? []);
        if (soleProfile) {
          selectProfile(soleProfile);
        }
      })
      .catch(() => {});

    return () => {
      cancelled = true;
    };
  }, [profile, selectProfile, user]);

  const login = useCallback(
    async (username: string, password: string, provider?: string) => {
      const tokens = await v2("POST /api/v2/auth/login", {
        body: { username, password, provider },
      });
      const session = sessionFromTokenPair(tokens);
      applyAuthenticatedUser(session);
      return session.user;
    },
    [applyAuthenticatedUser],
  );

  // Tokens carry the temporary-password restriction from when they were
  // issued; the server drops it from the ones a refresh issues once the
  // account has a new password. Rotation keeps the session identity, so a
  // sign-out meanwhile still discards the result.
  const settleTemporaryPassword = useCallback(async () => {
    const session = captureSessionIdentity();
    if (!(await refreshAuthentication())) {
      throw new Error("Your password was changed, but the session ended. Sign in again.");
    }
    const account = userFromAccount(await v2("GET /api/v2/account/me"));
    if (!isSessionIdentityCurrent(session)) return;
    setUser(account);
  }, []);

  // An admin can change an account's permissions or download policy without
  // signing it out, so the account the app gates features on is re-read when
  // the server reports an access change. An unchanged account keeps its
  // identity so nothing keyed on it re-renders. Several triggers can re-read
  // it at once (access_changed, a role change, the focus catch-up); a read
  // applies unless a newer one already has, so a newer read that fails does
  // not discard an older one that succeeded.
  const accountReadsRef = useRef({ started: 0, applied: 0 });
  const refreshAccount = useCallback(async () => {
    const session = captureSessionIdentity();
    const reads = accountReadsRef.current;
    const read = ++reads.started;
    const next = userFromAccount(await v2("GET /api/v2/account/me"));
    if (!isSessionIdentityCurrent(session) || read < reads.applied) return;
    reads.applied = read;
    setUser((current) => {
      if (!current || current.id !== next.id) return current;
      return JSON.stringify(current) === JSON.stringify(next) ? current : next;
    });
  }, []);

  // An admin changed the account's role: the server refused the old access
  // token, and the client already refreshed it without signing out. Re-read
  // the account so admin controls appear or disappear with the new role.
  useEffect(() => {
    onRoleChanged(() => void refreshAccount().catch(() => {}));
    return () => onRoleChanged(null);
  }, [refreshAccount]);

  const setupInitialUser = useCallback(
    async (username: string, email: string, password: string) => {
      const tokens = await v2("POST /api/v2/auth/setup", {
        body: { username, email, password, create_default_profile: true },
      });
      const session = sessionFromTokenPair(tokens);
      // The default profile is created with the account. Select it in the
      // same batch as the user so profile-scoped work (the wizard's settings
      // reads) can start on the first render, instead of waiting for the
      // sole-profile bootstrap effect to run a render later.
      setAccessToken(session.access_token);
      setRefreshToken(session.refresh_token);
      const created = getBootstrapProfile((await listProfiles().catch(() => null))?.profiles ?? []);
      applyAuthenticatedUser(session);
      if (created) selectProfile(created);
    },
    [applyAuthenticatedUser, selectProfile],
  );

  const signup = useCallback(
    async (username: string, email: string, password: string, inviteCode: string) => {
      const tokens = await v2("POST /api/v2/auth/signup", {
        body: { username, email, password, invite_code: inviteCode, create_default_profile: true },
      });
      applyAuthenticatedUser(sessionFromTokenPair(tokens));
    },
    [applyAuthenticatedUser],
  );

  return (
    <AuthContext.Provider
      value={{
        user,
        pendingPasswordChange,
        profile,
        loading,
        setupLoading,
        setupRequired,
        setupCompleted,
        refreshSetupStatus,
        providers,
        refreshSignInProviders,
        sessionRestoreUnavailable,
        sessionRestoreProviderUnavailable,
        retrySessionRestore,
        isImpersonating,
        login,
        settleTemporaryPassword,
        refreshAccount,
        completeLogin: applyAuthenticatedUser,
        setupInitialUser,
        signup,
        beginImpersonation,
        endImpersonation,
        logout,
        logoutOfSiloOnly,
        selectProfile,
        verifyProfilePin,
        clearProfile,
      }}
    >
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}

export function useOptionalAuth(): AuthState | null {
  return useContext(AuthContext);
}
