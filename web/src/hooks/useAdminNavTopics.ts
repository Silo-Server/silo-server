import {
  createContext,
  createElement,
  useCallback,
  useContext,
  useEffect,
  useState,
  type Dispatch,
  type ReactNode,
  type SetStateAction,
} from "react";

/** Topics the admin opened or closed by hand while at `place`. */
interface TopicChoices {
  place: string | null;
  choices: Record<string, boolean>;
}

type TopicStore = [TopicChoices, Dispatch<SetStateAction<TopicChoices>>];

const NO_CHOICES: TopicChoices = { place: null, choices: {} };

const AdminNavTopicsContext = createContext<TopicStore | null>(null);

/**
 * Holds the topic choices for the admin shell, so the desktop sidebar and the
 * phone drawer share them and closing the drawer does not drop them. It lives
 * only as long as the admin shell, so a new visit starts fresh.
 */
export function AdminNavTopicsProvider({ children }: { children: ReactNode }) {
  const store = useState(NO_CHOICES);
  return createElement(AdminNavTopicsContext.Provider, { value: store }, children);
}

/**
 * Which admin sidebar topics are open. The topic holding the current page is
 * open; the admin can open or close any other topic beside it. Arriving in a
 * different topic, or onto another page outside every topic, folds the rest
 * away again, and nothing is kept across reloads or visits.
 *
 * `place` names where the admin is for that reset: the active topic's id, or
 * the page path when the page belongs to no topic.
 */
export function useAdminNavTopics(activeTopicId: string | null, place: string) {
  const local = useState(NO_CHOICES);
  const [stored, setStored] = useContext(AdminNavTopicsContext) ?? local;
  // Choices made elsewhere are ignored while rendering, so arriving never
  // paints one frame with the old topics still open; the effect then drops
  // them so coming back later starts fresh too.
  const choices = stored.place === place ? stored.choices : NO_CHOICES.choices;
  useEffect(() => {
    setStored((current) => (current.place === place ? current : { place, choices: {} }));
  }, [place, setStored]);

  const isOpen = useCallback(
    (topicId: string) => choices[topicId] ?? topicId === activeTopicId,
    [activeTopicId, choices],
  );

  const toggle = useCallback(
    (topicId: string) => setStored({ place, choices: { ...choices, [topicId]: !isOpen(topicId) } }),
    [choices, isOpen, place, setStored],
  );

  return { isOpen, toggle };
}
