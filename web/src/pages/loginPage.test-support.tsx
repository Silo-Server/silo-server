import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { expect, type Mock } from "vitest";
import Login from "./Login";

/** Renders the login page at `entry` under its own query client. */
export function renderLogin(
  entry = "/login",
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } }),
) {
  const view = render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[entry]}>
        <Login />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return { ...view, client };
}

/** Types a username and password and presses Sign in. */
export function signInWithPassword(username: string, password = "pw") {
  fireEvent.change(screen.getByLabelText("Username"), { target: { value: username } });
  fireEvent.change(screen.getByLabelText("Password"), { target: { value: password } });
  fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
}

/** Waits for the password-reset capability read, the page's last request. */
export async function waitForPasswordResetCapability(request: Mock) {
  await waitFor(() =>
    expect(request.mock.calls.map(([operation]) => operation)).toContain(
      "GET /api/v2/capabilities/password-reset",
    ),
  );
}
