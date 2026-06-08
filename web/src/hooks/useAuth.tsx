import { createContext, useCallback, useContext, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import {
  api,
  ApiClientError,
  bootstrapAccessToken,
  getAccessToken,
  onProfileUnverified,
  restoreUserSession,
  setAccessToken,
  setProfileId,
  setProfileToken,
  setRefreshToken,
} from "@/api/client";
import { storage } from "@/utils/storage";
import type {
  AuthProviderOption,
  LoginResponse,
  Profile,
  SetupRequest,
  SetupStatusResponse,
  SignupRequest,
  User,
  VerifyPinResponse,
} from "@/api/types";
import { queryClient } from "@/lib/query-client";
import {
  clearStoredImpersonationAdminSession,
  loadStoredImpersonationAdminSession,
  saveStoredImpersonationAdminSession,
  type StoredImpersonationAdminSession,
} from "@/lib/impersonationSession";

interface AuthState {
  user: User | null;
  profile: Profile | null;
  loading: boolean;
  setupLoading: boolean;
  setupRequired: boolean;
  providers: AuthProviderOption[];
  isImpersonating: boolean;
  login: (username: string, password: string, provider?: string) => Promise<void>;
  completeLogin: (data: LoginResponse) => void;
  setupInitialUser: (username: string, email: string, password: string) => Promise<void>;
  signup: (username: string, email: string, password: string, inviteCode: string) => Promise<void>;
  beginImpersonation: (data: LoginResponse, returnPath: string) => void;
  endImpersonation: () => Promise<void>;
  logout: () => void;
  selectProfile: (profile: Profile, profileToken?: string) => void;
  verifyProfilePin: (profileId: string, pin: string) => Promise<VerifyPinResponse>;
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
  if (!(error instanceof ApiClientError)) {
    return false;
  }

  if (error.status === 401) {
    return true;
  }

  return error.status === 400 && error.code === "not_impersonating";
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
}: {
  refreshToken: string | null;
  hasStoredImpersonationAdminSession: boolean;
  bootstrapAccessToken: () => Promise<boolean>;
  fetchCurrentUser: () => Promise<TUser>;
  applyCurrentUser: (user: TUser) => void;
  restoreProfile: () => void;
  recoverPreservedAdminSession: () => Promise<boolean>;
  clearTokens: () => void;
  clearActiveAuthState: () => void;
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
  const [user, setUser] = useState<User | null>(null);
  const [profile, setProfile] = useState<Profile | null>(null);
  const [loading, setLoading] = useState(true);
  const [setupLoading, setSetupLoading] = useState(true);
  const [setupRequired, setSetupRequired] = useState(false);
  const [providers, setProviders] = useState<AuthProviderOption[]>([]);
  const isImpersonating = Boolean(user?.impersonation?.active);
  const soleProfileBootstrapRef = useRef<string | null>(null);

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
      setAccessToken(data.access_token);
      setRefreshToken(data.refresh_token);
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
    async (storedSession: { accessToken: string; refreshToken: string }) => {
      const restoredSession = await restoreUserSession<User>(storedSession);
      clearProfile();
      queryClient.clear();
      setAccessToken(restoredSession.accessToken);
      setRefreshToken(restoredSession.refreshToken);
      clearStoredImpersonationAdminSession();
      setUser(restoredSession.user);
      setSetupRequired(false);
    },
    [clearProfile],
  );

  const recoverPreservedAdminSession = useCallback(async () => {
    const storedSession = loadStoredImpersonationAdminSession();
    if (!storedSession) {
      return false;
    }

    await restoreAdminUser(storedSession);
    return true;
  }, [restoreAdminUser]);

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
      endImpersonationRequest: () => api("/auth/impersonation/end", { method: "POST" }),
      loadStoredImpersonationAdminSession,
      restoreAdminUser,
      clearAuthState,
      clearActiveAuthState,
    });
  }, [clearActiveAuthState, clearAuthState, restoreAdminUser]);

  const logout = useCallback(() => {
    // Fire and forget the server logout
    if (getAccessToken()) {
      api("/auth/logout", { method: "POST" }).catch(() => {});
    }
    clearAuthState();
  }, [clearAuthState]);

  const verifyProfilePin = useCallback(
    async (profileId: string, pin: string): Promise<VerifyPinResponse> => {
      return api<VerifyPinResponse>(`/profiles/${profileId}/verify-pin`, {
        method: "POST",
        body: JSON.stringify({ pin }),
      });
    },
    [],
  );

  const selectProfile = useCallback((p: Profile, profileToken?: string) => {
    setProfileId(p.id);
    setProfileToken(profileToken ?? null);
    storage.set(storage.KEYS.CURRENT_PROFILE, JSON.stringify(p));
    setProfile(p);
  }, []);

  useEffect(() => {
    onProfileUnverified(clearProfile);
    return () => onProfileUnverified(null);
  }, [clearProfile]);

  useEffect(() => {
    let cancelled = false;

    async function initialize() {
      try {
        const [status, availableProviders] = await Promise.all([
          api<SetupStatusResponse>("/auth/setup"),
          api<AuthProviderOption[]>("/auth/providers"),
        ]);
        if (cancelled) {
          return;
        }
        setSetupRequired(status.needs_setup);
        setProviders(availableProviders ?? []);
      } catch {
        if (!cancelled) {
          setSetupRequired(false);
          setProviders([]);
        }
      } finally {
        if (!cancelled) {
          setSetupLoading(false);
        }
      }

      try {
        await initializeAuthSession({
          refreshToken: storage.get(storage.KEYS.REFRESH_TOKEN),
          hasStoredImpersonationAdminSession: Boolean(loadStoredImpersonationAdminSession()),
          bootstrapAccessToken: () => bootstrapAccessToken(),
          fetchCurrentUser: () => api<User>("/auth/me"),
          applyCurrentUser: (currentUser) => {
            if (cancelled) {
              return;
            }
            setUser(currentUser);
          },
          restoreProfile: () => {
            if (cancelled) {
              return;
            }
            restoreProfile();
          },
          recoverPreservedAdminSession: async () => {
            if (cancelled) {
              return false;
            }
            return recoverPreservedAdminSession();
          },
          clearTokens: () => {
            if (cancelled) {
              return;
            }
            setAccessToken(null);
            setRefreshToken(null);
          },
          clearActiveAuthState: () => {
            if (cancelled) {
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

    initialize();

    return () => {
      cancelled = true;
    };
  }, [clearActiveAuthState, recoverPreservedAdminSession, restoreProfile]);

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
    api<{ profiles: Profile[] }>("/profiles")
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
      const data = await api<LoginResponse>("/auth/login", {
        method: "POST",
        body: JSON.stringify({ username, password, provider }),
      });
      applyAuthenticatedUser(data);
    },
    [applyAuthenticatedUser],
  );

  const setupInitialUser = useCallback(
    async (username: string, email: string, password: string) => {
      const body: SetupRequest = {
        username,
        email,
        password,
        create_default_profile: true,
      };
      const data = await api<LoginResponse>("/auth/setup", {
        method: "POST",
        body: JSON.stringify(body),
      });
      applyAuthenticatedUser(data);
    },
    [applyAuthenticatedUser],
  );

  const signup = useCallback(
    async (username: string, email: string, password: string, inviteCode: string) => {
      const body: SignupRequest = {
        username,
        email,
        password,
        invite_code: inviteCode,
        create_default_profile: true,
      };
      const data = await api<LoginResponse>("/auth/signup", {
        method: "POST",
        body: JSON.stringify(body),
      });
      applyAuthenticatedUser(data);
    },
    [applyAuthenticatedUser],
  );

  return (
    <AuthContext.Provider
      value={{
        user,
        profile,
        loading,
        setupLoading,
        setupRequired,
        providers,
        isImpersonating,
        login,
        completeLogin: applyAuthenticatedUser,
        setupInitialUser,
        signup,
        beginImpersonation,
        endImpersonation,
        logout,
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
