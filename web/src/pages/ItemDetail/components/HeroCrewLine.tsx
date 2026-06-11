import { Link } from "react-router";
import type { CrewMember } from "@/api/types";
import { buildPersonCatalogHref } from "@/pages/catalogSearchParams";

interface HeroCrewLineProps {
  crew: CrewMember[];
  genres?: string[];
  jobLabel?: string;
}

interface CrewPerson {
  name: string;
  personId: string;
}

function CrewNames({ people }: { people: CrewPerson[] }) {
  return (
    <>
      {people.map((p, i) => (
        <span key={p.name}>
          {i > 0 && ", "}
          {p.personId ? (
            <Link
              to={buildPersonCatalogHref(p.personId)}
              className="text-foreground/70 hover:text-foreground/90 font-medium transition-colors"
            >
              {p.name}
            </Link>
          ) : (
            <span className="text-foreground/70 font-medium">{p.name}</span>
          )}
        </span>
      ))}
    </>
  );
}

export default function HeroCrewLine({
  crew,
  genres,
  jobLabel = "Directed by",
}: HeroCrewLineProps) {
  const directors = crew
    .filter((c) => c.job === "Director")
    .map((c): CrewPerson => ({ name: c.name, personId: c.person_id }))
    .slice(0, 2);

  const writers = crew
    .filter((c) => c.job === "Writer" || c.job === "Screenplay")
    .map((c): CrewPerson => ({ name: c.name, personId: c.person_id }))
    .slice(0, 2);

  // Book/manga credits: shown when the item has Author people (video items
  // never do, so the section simply doesn't render there).
  const authors = crew
    .filter((c) => c.job === "Author")
    .map((c): CrewPerson => ({ name: c.name, personId: c.person_id }))
    .slice(0, 3);

  const hasDirectors = directors.length > 0;
  const hasWriters = writers.length > 0;
  const hasAuthors = authors.length > 0;
  const hasGenres = genres && genres.length > 0;

  if (!hasDirectors && !hasWriters && !hasAuthors && !hasGenres) return null;

  return (
    <div className="text-muted-foreground text-[13px]">
      {hasAuthors && (
        <>
          <span className="text-muted-foreground/60">By </span>
          <CrewNames people={authors} />
        </>
      )}
      {hasAuthors && (hasDirectors || hasWriters || hasGenres) && (
        <span className="text-muted-foreground/40 mx-2">&middot;</span>
      )}
      {hasDirectors && (
        <>
          <span className="text-muted-foreground/60">{jobLabel} </span>
          <CrewNames people={directors} />
        </>
      )}
      {hasDirectors && hasWriters && (
        <span className="text-muted-foreground/40 mx-2">&middot;</span>
      )}
      {hasWriters && (
        <>
          <span className="text-muted-foreground/60">Written by </span>
          <CrewNames people={writers} />
        </>
      )}
      {(hasDirectors || hasWriters) && hasGenres && (
        <span className="text-muted-foreground/40 mx-2">&middot;</span>
      )}
      {hasGenres &&
        genres.map((g, i) => (
          <span key={g}>
            <span className="text-foreground/60">{g}</span>
            {i < genres.length - 1 && (
              <span className="text-muted-foreground/40 mx-1.5">&middot;</span>
            )}
          </span>
        ))}
    </div>
  );
}
