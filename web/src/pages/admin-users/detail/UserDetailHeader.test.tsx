// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeAll, expect, it, vi } from "vitest";
import { makeAdminUser, stubRadixDom } from "@/pages/adminPage.test-support";
import { CardEditingProvider } from "./cardEditing";
import { UserDetailHeader } from "./UserDetailHeader";

beforeAll(stubRadixDom);
afterEach(cleanup);

function mount(localPasswordAllowed: boolean) {
  const noop = vi.fn();
  render(
    <MemoryRouter>
      <CardEditingProvider>
        <UserDetailHeader
          user={makeAdminUser({ local_password_allowed: localPasswordAllowed })}
          manageable
          available
          viewAsDisabled={false}
          transferable={false}
          ownAccount={false}
          onViewAs={noop}
          onResetPassword={noop}
          onSetPassword={noop}
          onCreateSignInLink={noop}
          onTransfer={noop}
          onDisable={noop}
          onEnable={noop}
          onDelete={noop}
        />
      </CardEditingProvider>
    </MemoryRouter>,
  );
}

// The action is offered exactly for an account the server would make a link
// for: one it lets sign in with a local password (local_password_allowed,
// the rule password sign-in uses), such as a break-glass administrator while
// the server's switch is off, and not for one it would refuse.
it.each([
  [true, "offered"],
  [false, "not offered"],
])("with local_password_allowed %s the sign-in link action is %s", async (allowed) => {
  const ui = userEvent.setup();
  mount(allowed);
  await ui.click(screen.getByRole("button", { name: "More actions" }));
  const item = screen.queryByRole("menuitem", { name: /Create sign-in link/ });
  expect(item !== null).toBe(allowed);
});
