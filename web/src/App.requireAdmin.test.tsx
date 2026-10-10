import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Profile, User } from "@/api/types";
import type { useAuth } from "@/hooks/useAuth";

type AuthState = ReturnType<typeof useAuth>;

let auth: Pick<AuthState, "user" | "profile">;

vi.mock("@/hooks/useAuth", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/useAuth")>("@/hooks/useAuth");
  return { ...actual, useAuth: () => auth as AuthState };
});

vi.mock("@/hooks/useIsActingAdmin", async () => {
  const { isActingAdmin } =
    await vi.importActual<typeof import("@/lib/permissions")>("@/lib/permissions");
  return { useIsActingAdmin: () => isActingAdmin(auth.user, auth.profile) };
});

import { RequireAdmin } from "@/App";

function makeUser(role: User["role"]): User {
  return { id: 1, username: "admin", email: "", role, permissions: [], download_allowed: false };
}

function makeProfile(isPrimary: boolean): Profile {
  return { id: "p-1", name: "Parent", is_primary: isPrimary } as Profile;
}

function Where() {
  const location = useLocation();
  return <p>{`at ${location.pathname}${location.search}`}</p>;
}

function renderAt(path: string) {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route
          path="/admin/*"
          element={
            <RequireAdmin>
              <p>admin area</p>
            </RequireAdmin>
          }
        />
        <Route path="*" element={<Where />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("RequireAdmin", () => {
  beforeEach(() => {
    auth = { user: null, profile: null };
  });

  it("opens the admin area for the admin's primary profile", () => {
    auth = { user: makeUser("admin"), profile: makeProfile(true) };
    renderAt("/admin/users");
    expect(screen.getByText("admin area")).toBeInTheDocument();
  });

  it("sends an admin with no profile to the picker and back", () => {
    auth = { user: makeUser("admin"), profile: null };
    renderAt("/admin/users?page=2");
    expect(
      screen.getByText(`at /profiles?redirect=${encodeURIComponent("/admin/users?page=2")}`),
    ).toBeInTheDocument();
  });

  it("keeps a non-primary profile out", () => {
    auth = { user: makeUser("admin"), profile: makeProfile(false) };
    renderAt("/admin/users");
    expect(screen.getByText("at /")).toBeInTheDocument();
  });

  it("keeps a regular account out", () => {
    auth = { user: makeUser("user"), profile: null };
    renderAt("/admin/users");
    expect(screen.getByText("at /")).toBeInTheDocument();
  });
});
