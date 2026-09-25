import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Profile, User } from "@/api/types";
import type { useAuth } from "@/hooks/useAuth";

type AuthState = ReturnType<typeof useAuth>;

let auth: Pick<AuthState, "user" | "profile">;

vi.mock("@/hooks/useAuth", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/useAuth")>("@/hooks/useAuth");
  return { ...actual, useAuth: () => auth as AuthState };
});

import { QueryCacheManager } from "@/App";

function makeUser(id: number): User {
  return {
    id,
    username: `user-${id}`,
    email: "",
    role: "user",
    permissions: [],
    download_allowed: false,
  };
}

function makeProfile(id: string): Profile {
  return { id, name: id } as Profile;
}

function renderManager() {
  const queryClient = new QueryClient();
  const clear = vi.spyOn(queryClient, "clear");
  const view = render(
    <QueryClientProvider client={queryClient}>
      <QueryCacheManager />
    </QueryClientProvider>,
  );
  const signIn = (next: Pick<AuthState, "user" | "profile">) => {
    auth = next;
    view.rerender(
      <QueryClientProvider client={queryClient}>
        <QueryCacheManager />
      </QueryClientProvider>,
    );
  };
  return { clear, signIn };
}

describe("QueryCacheManager", () => {
  beforeEach(() => {
    auth = { user: null, profile: null };
  });

  it("keeps the cache while a stored session restores at boot", () => {
    const { clear, signIn } = renderManager();

    // Boot renders with no user until the restore lands; the shell's reads
    // started in that window must survive it.
    signIn({ user: makeUser(1), profile: makeProfile("p-1") });

    expect(clear).not.toHaveBeenCalled();
  });

  it("clears the cache on sign-out", () => {
    const { clear, signIn } = renderManager();
    signIn({ user: makeUser(1), profile: makeProfile("p-1") });

    signIn({ user: null, profile: null });

    expect(clear).toHaveBeenCalledTimes(1);
  });

  it("leaves an account change to AuthProvider", () => {
    const { clear, signIn } = renderManager();
    signIn({ user: makeUser(1), profile: makeProfile("p-1") });

    // AuthProvider clears before the new account renders; clearing again
    // here would discard the new account's first reads.
    signIn({ user: makeUser(2), profile: null });

    expect(clear).not.toHaveBeenCalled();
  });
});
