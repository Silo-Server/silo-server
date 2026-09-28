import { describe, expect, it } from "vitest";
import type { AdminUser } from "@/api/types";
import { canManageAccount, canTransferOwnership } from "./accountOwner";

const account = (overrides: Partial<AdminUser>): AdminUser =>
  ({ id: 5, role: "user", enabled: true, is_owner: false, ...overrides }) as AdminUser;
const owner = account({ id: 1, role: "admin", is_owner: true });
const admin = account({ id: 2, role: "admin" });
const user = account({ id: 3 });

describe("canManageAccount", () => {
  it("lets only the owner manage other admins", () => {
    expect(canManageAccount(admin, 9, false)).toBe(false);
    expect(canManageAccount(admin, owner.id, true)).toBe(true);
    expect(canManageAccount(admin, admin.id, false)).toBe(true);
  });

  it("keeps everyone else off the owner", () => {
    expect(canManageAccount(owner, admin.id, false)).toBe(false);
    expect(canManageAccount(owner, owner.id, true)).toBe(true);
  });

  it("lets any admin manage ordinary accounts", () => {
    expect(canManageAccount(user, admin.id, false)).toBe(true);
  });
});

describe("canTransferOwnership", () => {
  it("offers ownership of another enabled admin to the owner only", () => {
    expect(canTransferOwnership(admin, owner.id, true)).toBe(true);
    expect(canTransferOwnership(admin, 9, false)).toBe(false);
    expect(canTransferOwnership(user, owner.id, true)).toBe(false);
    expect(canTransferOwnership({ ...admin, enabled: false }, owner.id, true)).toBe(false);
    expect(canTransferOwnership(owner, owner.id, true)).toBe(false);
  });
});
