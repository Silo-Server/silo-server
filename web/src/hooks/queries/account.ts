import { useMutation, useQuery } from "@tanstack/react-query";

import { captureProfileRequestContext } from "@/api/client";
import { v2, type V2Body, type V2Result } from "@/api/v2/request";

/** Whether and within which limits the caller may replace the account password. */
export type AccountPasswordCapability = V2Result<"GET /api/v2/account/password/capability">;

export const accountKeys = {
  passwordCapability: () => ["account", "password-capability"] as const,
};

export function useAccountPasswordCapability() {
  return useQuery({
    queryKey: accountKeys.passwordCapability(),
    queryFn: () => v2("GET /api/v2/account/password/capability"),
  });
}

export function useChangeAccountPassword() {
  return useMutation({
    mutationFn: (body: V2Body<"POST /api/v2/account/password">) => {
      // Bind the write to the profile that was active when the user submitted:
      // a household profile switch while it is in flight must not re-author it.
      const profileContext = captureProfileRequestContext();
      return profileContext
        ? v2("POST /api/v2/account/password", { body, profileContext })
        : v2("POST /api/v2/account/password", { body });
    },
  });
}
