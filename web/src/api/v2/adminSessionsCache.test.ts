import { beforeEach, expect, it } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { captureProfileRequestContext, setAccessToken, setProfileId } from "@/api/client";
import { installPolicyStorageMocks } from "@/pages/admin-policy/policyTestUtils";
import { adminSessionsKey } from "./adminSessionsCache";

beforeEach(() => {
  installPolicyStorageMocks();
  setAccessToken("admin");
  setProfileId("primary");
});
it("shares the captured key while keeping account and profile caches distinct", () => {
  const client = new QueryClient();
  const primary = captureProfileRequestContext();
  const key = adminSessionsKey(primary);
  client.setQueryData(key, ["primary-session"]);
  expect(client.getQueryData(adminSessionsKey(primary))).toEqual(["primary-session"]);
  setProfileId("child");
  expect(client.getQueryData(adminSessionsKey(captureProfileRequestContext()))).toBeUndefined();
  setAccessToken("other-account");
  expect(client.getQueryData(adminSessionsKey(captureProfileRequestContext()))).toBeUndefined();
  expect(client.getQueryData(key)).toEqual(["primary-session"]);
});
