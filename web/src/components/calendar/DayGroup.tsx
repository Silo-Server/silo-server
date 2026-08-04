import MediaCarousel from "@/components/MediaCarousel";
import { formatDayHeading, isToday } from "@/lib/calendarWeek";
import CalendarEventCard from "./CalendarEventCard";
import type { CalendarDay } from "@/hooks/queries/calendar";
import { useUICustomization } from "@/hooks/useUICustomization";
import { carouselCardWidthClasses } from "@/lib/uiCustomization";

interface DayGroupProps {
  day: CalendarDay;
  isSelected?: boolean;
}

export default function DayGroup({ day, isSelected }: DayGroupProps) {
  const today = isToday(day.date);
  const title = formatDayHeading(day.date);
  const { cardPresentation } = useUICustomization();
  const posterWidthClasses = carouselCardWidthClasses(cardPresentation.poster_size);

  return (
    // scroll-mt offsets the sticky header so the title isn't hidden when scrolled into view
    // (mobile carries the app header + week strip; desktop only the week strip).
    <div
      id={`day-${day.date}`}
      data-selected={isSelected ? "true" : undefined}
      className="scroll-mt-44 transition-colors lg:scroll-mt-32"
    >
      <MediaCarousel
        title={title}
        headerActions={
          <span className="flex items-center gap-1.5">
            {today && (
              <span className="bg-primary text-primary-foreground rounded-full px-2 py-0.5 text-[10px] font-semibold uppercase">
                Today
              </span>
            )}
            {isSelected && (
              <span className="border-primary/40 text-primary rounded-full border px-2 py-0.5 text-[10px] font-semibold uppercase">
                Focused
              </span>
            )}
          </span>
        }
      >
        {day.items.map((event) => (
          <div key={event.content_id} className={posterWidthClasses}>
            <CalendarEventCard event={event} />
          </div>
        ))}
      </MediaCarousel>
    </div>
  );
}
