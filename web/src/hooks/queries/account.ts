import { useMutation, useQuery } from "@tanstack/react-query";

import { api, apiWithProfileRequestContext, captureProfileRequestContext } from "@/api/client";
import type { AccountPasswordCapability } from "@/api/types";

export const accountKeys = {
  passwordCapability: () => ["account", "password-capability"] as const,
};

export function useAccountPasswordCapability() {
  return useQuery({
    queryKey: accountKeys.passwordCapability(),
    queryFn: () => api<AccountPasswordCapability>("/auth/account/capability"),
  });
}

export function useChangeAccountPassword() {
  return useMutation({
    mutationFn: (body: { current_password: string; new_password: string }) => {
      const requestContext = captureProfileRequestContext();
      const options = {
        method: "POST",
        body: JSON.stringify(body),
      };
      return requestContext
        ? apiWithProfileRequestContext<void>("/auth/account/password", requestContext, options)
        : api<void>("/auth/account/password", options);
    },
  });
}
