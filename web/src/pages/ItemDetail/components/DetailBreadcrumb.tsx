import { Link } from "react-router";
import { ChevronRight } from "lucide-react";

interface BreadcrumbSegment {
  label: string;
  href?: string;
}

interface DetailBreadcrumbProps {
  segments: BreadcrumbSegment[];
}

export default function DetailBreadcrumb({ segments }: DetailBreadcrumbProps) {
  if (segments.length === 0) return null;

  return (
    <nav aria-label="Breadcrumb">
      <div className="flex items-center gap-1.5">
        {segments.map((segment, i) => {
          const isLast = i === segments.length - 1;
          return (
            <span key={i} className="flex items-center gap-1.5">
              {i > 0 && <ChevronRight className="text-muted-foreground/40 size-4" />}
              {segment.href && !isLast ? (
                <Link
                  to={segment.href}
                  className="text-muted-foreground hover:text-foreground text-[13px] transition-colors"
                >
                  {segment.label}
                </Link>
              ) : (
                <span
                  className="text-muted-foreground/60 text-[13px]"
                  {...(isLast ? { "aria-current": "page" as const } : {})}
                >
                  {segment.label}
                </span>
              )}
            </span>
          );
        })}
      </div>
    </nav>
  );
}
