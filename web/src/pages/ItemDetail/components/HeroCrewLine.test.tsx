import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import type { CrewMember } from "@/api/types";
import HeroCrewLine from "./HeroCrewLine";

const SERIES_LEAD_JOBS = ["Creator", "Director"] as const;

const creator: CrewMember = { name: "Vince Gilligan", job: "Creator", person_id: "creator-1" };
const director: CrewMember = {
  name: "Michelle MacLaren",
  job: "Director",
  person_id: "director-1",
};

function render(crew: CrewMember[], props: Partial<Parameters<typeof HeroCrewLine>[0]> = {}) {
  return renderToStaticMarkup(
    <MemoryRouter>
      <HeroCrewLine crew={crew} {...props} />
    </MemoryRouter>,
  );
}

describe("HeroCrewLine", () => {
  it("leads a series with its creators ahead of its directors", () => {
    const markup = render([director, creator], {
      jobLabel: "Created by",
      leadJobs: SERIES_LEAD_JOBS,
    });

    expect(markup).toContain("Created by");
    expect(markup).toContain("Vince Gilligan");
    expect(markup).not.toContain("Michelle MacLaren");
  });

  it("falls back to directors for a series without Creator credits", () => {
    const markup = render([director], { jobLabel: "Created by", leadJobs: SERIES_LEAD_JOBS });

    expect(markup).toContain("Created by");
    expect(markup).toContain("Michelle MacLaren");
  });

  it("keeps a series creator out of the default Directed by line", () => {
    const markup = render([creator, director]);

    expect(markup).toContain("Directed by");
    expect(markup).toContain("Michelle MacLaren");
    expect(markup).not.toContain("Vince Gilligan");
  });
});
