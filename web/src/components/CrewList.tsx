import ViewTransitionLink from "@/components/ViewTransitionLink";
import type { CrewMember } from "@/api/types";
import { buildPersonCatalogHref } from "@/pages/catalogSearchParams";

interface CrewListProps {
  crew: CrewMember[];
  /** Jobs to list, in order. Others are ignored. Defaults to Director, Writer and Producer. */
  jobs?: readonly string[];
}

interface CrewEntry {
  name: string;
  personId: string;
}

/** Jobs to display, in order, when the page doesn't choose its own. */
const DISPLAY_JOBS = ["Director", "Writer", "Producer"] as const;

/**
 * Lists a title's crew by job, one row per job in `jobs` order, each person
 * once and linked to their person page. Renders nothing when no listed job has
 * credits.
 */
export default function CrewList({ crew, jobs = DISPLAY_JOBS }: CrewListProps) {
  if (crew.length === 0) return null;

  const grouped = new Map<string, CrewEntry[]>();
  for (const member of crew) {
    const existing = grouped.get(member.job);
    const entry: CrewEntry = { name: member.name, personId: member.person_id };
    if (existing) {
      if (!existing.some((e) => e.name === member.name)) {
        existing.push(entry);
      }
    } else {
      grouped.set(member.job, [entry]);
    }
  }

  const entries = jobs
    .filter((job) => grouped.has(job))
    .map((job) => ({
      job: `${job}s`,
      people: grouped.get(job)!,
    }));

  if (entries.length === 0) return null;

  return (
    <div>
      <h2 className="mb-4 text-xl font-semibold">Crew</h2>
      <dl className="glass-subtle grid grid-cols-[auto_1fr] gap-x-6 gap-y-3 rounded-xl px-5 py-4 text-sm">
        {entries.map(({ job, people }) => (
          <div key={job} className="contents">
            <dt className="text-muted-foreground text-[0.8125rem] font-medium">{job}</dt>
            <dd className="text-foreground/85 truncate">
              {people.map((p, i) => (
                <span key={p.name}>
                  {i > 0 && ", "}
                  {p.personId ? (
                    <ViewTransitionLink
                      to={buildPersonCatalogHref(p.personId)}
                      className="hover:text-primary transition-colors"
                    >
                      {p.name}
                    </ViewTransitionLink>
                  ) : (
                    p.name
                  )}
                </span>
              ))}
            </dd>
          </div>
        ))}
      </dl>
    </div>
  );
}
