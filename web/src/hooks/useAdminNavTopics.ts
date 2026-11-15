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

/** Topics the admin opened or closed by hand while in `topicId`. */
interface TopicChoices {
  topicId: string | null;
  choices: Record<string, boolean>;
}

type TopicStore = [TopicChoices, Dispatch<SetStateAction<TopicChoices>>];

const NO_CHOICES: TopicChoices = { topicId: null, choices: {} };

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
 * different topic folds the rest away again, and nothing is kept across
 * reloads or visits.
 */
export function useAdminNavTopics(activeTopicId: string | null) {
  const local = useState(NO_CHOICES);
  const [stored, setStored] = useContext(AdminNavTopicsContext) ?? local;
  // Choices made in another topic are ignored while rendering, so arriving in
  // a topic never paints one frame with the old topics still open; the effect
  // then drops them so returning to that topic later starts fresh too.
  const choices = stored.topicId === activeTopicId ? stored.choices : NO_CHOICES.choices;
  useEffect(() => {
    setStored((current) =>
      current.topicId === activeTopicId ? current : { topicId: activeTopicId, choices: {} },
    );
  }, [activeTopicId, setStored]);

  const isOpen = useCallback(
    (topicId: string) => choices[topicId] ?? topicId === activeTopicId,
    [activeTopicId, choices],
  );

  const toggle = useCallback(
    (topicId: string) =>
      setStored({ topicId: activeTopicId, choices: { ...choices, [topicId]: !isOpen(topicId) } }),
    [activeTopicId, choices, isOpen, setStored],
  );

  return { isOpen, toggle };
}
