interface NarratorCardProps {
  narrator: string;
}

function initials(name: string): string {
  return name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((w) => w[0]?.toUpperCase() ?? "")
    .join("");
}

export function NarratorCard({ narrator }: NarratorCardProps) {
  return (
    <section>
      <h2 className="mb-4 text-xl font-semibold tracking-tight">Narrator</h2>
      <div className="bg-muted/30 flex items-center gap-4 rounded-xl border p-4">
        <div className="bg-muted text-muted-foreground flex h-16 w-16 shrink-0 items-center justify-center rounded-full text-lg font-medium">
          {initials(narrator) || "?"}
        </div>
        <div className="min-w-0">
          <p className="truncate text-base font-medium">{narrator}</p>
        </div>
      </div>
    </section>
  );
}
