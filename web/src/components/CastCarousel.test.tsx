import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import CastCarousel from "./CastCarousel";
import { buildCrewGroups } from "./castCrewGroups";

describe("CastCarousel", () => {
  it("renders an Embla carousel with cast cards", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/series-1"]}>
        <CastCarousel
          cast={[
            {
              name: "Winona Ryder",
              character: "Joyce Byers",
              order: 1,
              person_id: "person-001",
              photo_url: "https://images.example.test/winona.jpg",
              photo_thumbhash: "thumbhash-cast",
            },
            {
              name: "David Harbour",
              character: "Jim Hopper",
              order: 2,
              person_id: "person-002",
            },
          ]}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain("embla__viewport");
    expect(markup).toContain("embla__container");
    expect(markup).toContain('href="/person/person-001"');
    expect(markup).toContain('src="https://images.example.test/winona.jpg"');
    expect(markup).toContain(">DH<");
    expect(markup).not.toContain('data-slot="scroll-area"');
  });

  it("puts crew groups ahead of the cast with labelled dividers", () => {
    const director = {
      name: "Denis Villeneuve",
      job: "Director",
      person_id: "person-100",
      photo_url: "https://images.example.test/villeneuve.jpg",
    };
    const crew = [
      director,
      director,
      { name: "Second Director", job: "Director", person_id: "person-101" },
      { name: "Third Director", job: "Director", person_id: "person-102" },
      { name: "Hampton Fancher", job: "Writer", person_id: "person-103" },
      { name: "Denis Villeneuve", job: "Writer", person_id: "person-100" },
      { name: "Some Producer", job: "Producer", person_id: "person-104" },
    ];
    const cast = Array.from({ length: 5 }, (_, i) => ({
      name: `Actor ${i}`,
      character: `Role ${i}`,
      order: i,
      person_id: `person-${i}`,
    }));
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <CastCarousel cast={cast} crewGroups={buildCrewGroups(crew, "Director")} limit={3} />
      </MemoryRouter>,
    );

    expect(markup.match(/>Denis Villeneuve</g)).toHaveLength(1);
    expect(markup).toContain(">Second Director<");
    expect(markup).not.toContain("Third Director");
    expect(markup).not.toContain("Some Producer");
    expect(markup).not.toContain("w-[140px]");
    expect(markup).toContain(">Writers<");
    expect(markup).toContain(">Cast<");
    const order = ["Denis Villeneuve", ">Writers<", "Hampton Fancher", ">Cast<", "Actor 0"].map(
      (needle) => markup.indexOf(needle),
    );
    expect(order).toEqual([...order].sort((a, b) => a - b));
    expect(markup).toContain("Actor 2");
    expect(markup).not.toContain("Actor 3");
  });

  it("labels series creators and renders crew alone when there is no cast", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/series-1"]}>
        <CastCarousel
          cast={[]}
          crewGroups={buildCrewGroups(
            [{ name: "Vince Gilligan", job: "Director", person_id: "person-200" }],
            "Creator",
          )}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain(">Vince Gilligan<");
    expect(markup).toContain(">Creator<");
    expect(markup).not.toContain(">Cast<");
    expect(markup).not.toContain("Writers");
  });

  it("leads a series with its Creator credits ahead of its directors", () => {
    const [leads] = buildCrewGroups(
      [
        { name: "Michelle MacLaren", job: "Director", person_id: "person-201" },
        { name: "Vince Gilligan", job: "Creator", person_id: "person-200" },
      ],
      "Creator",
    );

    expect(leads?.members.map((c) => c.name)).toEqual(["Vince Gilligan"]);
  });

  it("renders the plain cast row without dividers when no crew groups are passed", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <CastCarousel cast={[{ name: "Solo", character: "Lead", order: 0, person_id: "p" }]} />
      </MemoryRouter>,
    );

    expect(markup).toContain(">Solo<");
    expect(markup).not.toContain(">Cast<");
  });
});
