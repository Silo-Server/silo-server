import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { captureProfileRequestContext, captureSessionIdentity } from "@/api/client";
import { v2, type V2Result } from "@/api/v2/request";
import { useAuth } from "@/hooks/useAuth";

export type OwnAppKeyList = V2Result<"GET /api/v2/auth/keys">;
export type OwnAppDeviceKey = OwnAppKeyList["devices"][number];

/**
 * The signed-in account's own app keys and its device sign-ins approved in
 * advance, with the actions on them. Ask only once useAppKeySessionAccess
 * says the session may list them (`enabled`). Every action also refreshes
 * the login sessions: removing a device ends the sessions its key opened.
 */
export function useOwnAppKeys(enabled: boolean) {
  const client = useQueryClient();
  const session = captureSessionIdentity();
  const { profile } = useAuth();
  // Profile selection clears the shared query cache; the profile rebinds
  // the observers after it, as the login session list does.
  const scope = [session.serverOrigin, session.authContextVersion, profile?.id ?? null];
  const keysKey = ["own-app-keys", ...scope];
  const preapprovalsKey = ["own-device-preapprovals", ...scope];
  const keys = useQuery<OwnAppKeyList>({
    queryKey: keysKey,
    enabled,
    staleTime: 15_000,
    // A waiting device leaves the list when it is approved or its 24 hours end.
    refetchInterval: (query) =>
      query.state.data?.devices.some((key) => key.status === "pending") ? 30_000 : 60_000,
    queryFn: ({ signal }) => v2("GET /api/v2/auth/keys", { signal }),
  });
  const preapprovals = useQuery({
    queryKey: preapprovalsKey,
    enabled,
    staleTime: 15_000,
    refetchInterval: 60_000,
    queryFn: async ({ signal }) =>
      (await v2("GET /api/v2/auth/device/preapprovals", { signal })).items,
  });
  const settle = () => {
    void client.invalidateQueries({ queryKey: keysKey });
    void client.invalidateQueries({ queryKey: preapprovalsKey });
    void client.invalidateQueries({ queryKey: ["login-sessions"] });
  };
  const revokeDevice = useMutation({
    retry: false,
    mutationFn: (id: string) =>
      v2("DELETE /api/v2/auth/keys/devices/{id}", {
        path: { id },
        retryAuthentication: false,
      }),
    onSettled: settle,
  });
  const deny = useMutation({
    retry: false,
    mutationFn: (id: string) =>
      v2("POST /api/v2/auth/keys/pending/{id}/deny", {
        path: { id },
        body: {},
        retryAuthentication: false,
      }),
    onSettled: settle,
  });
  const approve = useMutation({
    retry: false,
    mutationFn: (id: string) => {
      // Bound to the profile active when the person asked: only the
      // household's main profile may approve.
      const profileContext = captureProfileRequestContext();
      return v2("POST /api/v2/auth/keys/pending/{id}/approve", {
        path: { id },
        body: {},
        retryAuthentication: false,
        ...(profileContext ? { profileContext } : {}),
      });
    },
    onSettled: settle,
  });
  const withdraw = useMutation({
    retry: false,
    mutationFn: (id: string) =>
      v2("DELETE /api/v2/auth/device/preapprovals/{id}", {
        path: { id },
        retryAuthentication: false,
      }),
    onSettled: settle,
  });
  return { keys, preapprovals, revokeDevice, deny, approve, withdraw };
}
