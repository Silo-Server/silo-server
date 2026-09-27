import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import getPersonOk from "../../../../contracts/api/v2/fixtures/get_person_ok.json";
import listPeopleOk from "../../../../contracts/api/v2/fixtures/list_people_ok.json";
import refreshPersonOk from "../../../../contracts/api/v2/fixtures/refresh_person_ok.json";

import { setProfileId } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import { getPerson, refreshPerson, searchPeople } from "./people";

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

  it("searches people and preserves string ids", async () => {
    const fetchMock = stubFetch(listPeopleOk);

    const people = await searchPeople("al", 5);

    expect(String(fetchMock.mock.calls[0]?.[0])).toBe("/api/v2/catalog/people?q=al&limit=5");
    expect(people).toEqual([{ ...listPeopleOk.items[0], id: "7" }]);
  });

  it("reads one person", async () => {
    const fetchMock = stubFetch(getPersonOk);

    const person = await getPerson("7");

    expect(String(fetchMock.mock.calls[0]?.[0])).toBe("/api/v2/catalog/people/7");
    expect(person.id).toBe("7");
    expect(person.name).toBe("Al Pacino");
  });

  it("sends the selected media scope with the people query", async () => {
    const fetchMock = stubFetch(listPeopleOk);
    await searchPeople("al", 5, { mediaScope: "video" });
    const url = new URL(String(fetchMock.mock.calls[0]?.[0]), "http://localhost");
    expect(url.searchParams.get("media_scope")).toBe("video");
    expect(url.searchParams.get("q")).toBe("al");
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

  it("queues a refresh and surfaces the accepted body", async () => {
    const fetchMock = stubFetch(refreshPersonOk, 202);

    const result = await refreshPerson("7");

    expect(String(fetchMock.mock.calls[0]?.[0])).toBe("/api/v2/catalog/people/7/refresh");
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBe("POST");
    expect(result).toEqual({ status: "queued", person_id: "7" });
  });
});
