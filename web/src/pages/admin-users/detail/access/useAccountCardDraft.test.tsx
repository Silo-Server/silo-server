// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";

import type { AdminUser } from "@/api/types";
import type { AdminUserEditor } from "@/api/v2/adminUsers";

import { useAccountCardDraft } from "./useAccountCardDraft";

const update = vi.hoisted(() => vi.fn());
vi.mock("@/hooks/queries/admin/users", () => ({
  useUpdateUser: () => ({ mutateAsync: update, isPending: false }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn() } }));

const user = { id: 7, username: "sample", requests_allowed: null } as unknown as AdminUser;
const editor = { user, etag: '"e"', profileContext: {} } as unknown as AdminUserEditor;

type Draft = { allowed: boolean | null; quota: number };

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={new QueryClient()}>{children}</QueryClientProvider>;
}

afterEach(() => vi.clearAllMocks());

it("says the extra write saved when the account change after it fails", async () => {
  update.mockRejectedValueOnce(new Error("Server error"));
  const write = vi.fn().mockResolvedValue(undefined);
  const { result } = renderHook(
    () =>
      useAccountCardDraft<Draft>({
        id: "requests",
        editor,
        toDraft: (u) => ({ allowed: u.requests_allowed, quota: 5 }),
        toBody: (d, base) =>
          d.allowed === base.requests_allowed ? {} : { requests_allowed: d.allowed },
        changedRows: () => ["Can request media", "Limit"],
        extra: {
          savedLabel: "Request approval and limit",
          changed: (d) => d.quota !== 5,
          write,
          reload: async () => undefined,
        },
      }),
    { wrapper },
  );
  act(() => result.current.start());
  act(() => result.current.setDraft(() => ({ allowed: false, quota: 9 })));
  let saved = true;
  await act(async () => {
    saved = await result.current.save();
  });
  expect(saved).toBe(false);
  expect(write).toHaveBeenCalledTimes(1);
  expect(result.current.error).toBe(
    "Request approval and limit saved. The account change wasn't: Server error",
  );
});
