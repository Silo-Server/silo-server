import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import getPersonOk from "../../../../contracts/api/v2/fixtures/get_person_ok.json";
import listPeopleOk from "../../../../contracts/api/v2/fixtures/list_people_ok.json";

import { setProfileId } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import { getPerson, searchPeople } from "./people";

type FetchMock = ReturnType<typeof vi.fn<typeof fetch>>;

function stubFetch(body: unknown, status = 200): FetchMock {
  const fetchMock = vi.fn<typeof fetch>(async () => jsonResponse(body, status));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("people on the v2 contract", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setProfileId("p-owner");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("preserves IDs larger than JavaScript's safe integer range across search and detail", async () => {
    const person = { ...getPersonOk, id: "137101642343383042" };
    const fetchMock = stubFetch({ ...listPeopleOk, items: [person] });
    const [match] = await searchPeople("Al Pacino");
    expect(match?.id).toBe(person.id);

    fetchMock.mockResolvedValueOnce(jsonResponse(person));
    const detail = await getPerson(String(match!.id));
    expect(String(fetchMock.mock.calls[1]?.[0])).toBe(`/api/v2/catalog/people/${person.id}`);
    expect(detail.id).toBe(person.id);
  });
});
