// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import SignedInSessions from "./SignedInSessions";

const capabilities = vi.hoisted(() => ({ data: {} as Record<string, unknown> }));
vi.mock("@/hooks/queries/loginSessions", () => ({
  useLoginSessionCapabilities: () => ({
    isPending: false,
    isError: false,
    data: capabilities.data,
    refetch: vi.fn(),
  }),
}));
vi.mock("@/components/sessions/LoginSessionsPanel", () => ({
  LoginSessionsPanel: ({ manageOthers }: { manageOthers?: boolean }) => (
    <p>manage others: {String(manageOthers)}</p>
  ),
}));
afterEach(cleanup);

// Demo mode answers false; a server that doesn't send the member yet
// (mid-upgrade) keeps other sessions manageable.
it.each([
  [false, "false"],
  [true, "true"],
  [undefined, "true"],
])("revoke_other_sessions %s lets the panel manage others: %s", (value, shown) => {
  capabilities.data = {
    available: true,
    ...(value === undefined ? {} : { revoke_other_sessions: value }),
  };
  render(<SignedInSessions />);
  expect(screen.getByText(`manage others: ${shown}`)).toBeInTheDocument();
});
