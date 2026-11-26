// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { SignInAgainNotice } from "./SignInAgainNotice";

const auth = vi.hoisted(() => ({ logoutOfSiloOnly: vi.fn() }));
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => auth }));

function Login() {
  const location = useLocation();
  return <p>Login {location.search}</p>;
}

function mount(signInAgain: boolean) {
  render(
    <MemoryRouter initialEntries={["/settings/sessions"]}>
      <Routes>
        <Route
          path="/settings/sessions"
          element={<SignInAgainNotice signInAgain={signInAgain} />}
        />
        <Route path="/login" element={<Login />} />
      </Routes>
    </MemoryRouter>,
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

// Signing in again must end only Silo's session (the provider's own stays,
// so a provider user comes straight back) and return to the same page.
it("ends this session and comes back to the page after signing in", async () => {
  mount(true);
  fireEvent.click(screen.getByRole("button", { name: "Sign in again" }));
  expect(auth.logoutOfSiloOnly).toHaveBeenCalledTimes(1);
  expect(await screen.findByText("Login ?redirect=%2Fsettings%2Fsessions")).toBeTruthy();
});

// A person with no other way into a browser would be stranded by signing out.
it("offers no action when signing in again would not help", () => {
  mount(false);
  expect(screen.getByRole("status")).toHaveTextContent("Manage your devices in the Silo app.");
  expect(screen.queryByRole("button")).toBeNull();
});
