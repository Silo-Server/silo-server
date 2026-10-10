// @vitest-environment jsdom
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { useLoginSessionCapabilities, useLoginSessions } from "./loginSessions";

const mocks = vi.hoisted(() => ({ profile: null as { id: string } | null, request: vi.fn() }));
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ profile: mocks.profile }) }));
vi.mock("@/api/v2/request", () => ({ v2: mocks.request }));
afterEach(cleanup);

it("resumes account session reads when automatic profile selection clears an in-flight query", async () => {
  mocks.profile = null;
  mocks.request.mockReset();
  let finishFirstCapability!: (value: unknown) => void;
  mocks.request.mockImplementation((operation: string) => {
    if (operation.endsWith("/capabilities")) {
      if (!finishFirstCapability)
        return new Promise((resolve) => {
          finishFirstCapability = resolve;
        });
      return Promise.resolve({ available: true });
    }
    return Promise.resolve({ items: [], current_session: null, page: { has_more: false } });
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  const hook = renderHook(
    () => {
      const capabilities = useLoginSessionCapabilities();
      const sessions = useLoginSessions(undefined, Boolean(capabilities.data?.available));
      return { capabilities, sessions };
    },
    { wrapper },
  );
  await waitFor(() => expect(mocks.request).toHaveBeenCalledTimes(1));
  act(() => {
    client.clear();
    mocks.profile = { id: "automatically-selected-profile" };
    hook.rerender();
    finishFirstCapability({ available: true });
  });
  await waitFor(() => expect(hook.result.current.sessions.query.isSuccess).toBe(true));
  expect(hook.result.current.capabilities.data?.available).toBe(true);
  expect(mocks.request).toHaveBeenCalledWith("GET /api/v2/auth/sessions", expect.anything());
});
