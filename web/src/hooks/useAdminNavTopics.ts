import { useCallback, useState } from "react";

type TopicChoices = Record<string, boolean>;

/**
 * Which admin sidebar topics are open. The topic holding the current page is
 * open; the admin can open or close any other topic beside it. Arriving in a
 * different topic folds the rest away again, and nothing is kept across
 * reloads or visits, so the sidebar always starts with just the current
 * page's topic open.
 */
export function useAdminNavTopics(activeTopicId: string | null) {
  const [choices, setChoices] = useState<TopicChoices>({});
  // Reset during render rather than in an effect, so arriving in a topic never
  // paints one frame with the old topics still open.
  const [arrivedIn, setArrivedIn] = useState(activeTopicId);
  if (arrivedIn !== activeTopicId) {
    setArrivedIn(activeTopicId);
    setChoices({});
  }

  const isOpen = useCallback(
    (topicId: string) => choices[topicId] ?? topicId === activeTopicId,
    [activeTopicId, choices],
  );

  const toggle = useCallback(
    (topicId: string) => setChoices((current) => ({ ...current, [topicId]: !isOpen(topicId) })),
    [isOpen],
  );

  return { isOpen, toggle };
}
