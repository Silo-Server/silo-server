import { useCallback, useState } from "react";

const OPEN_TOPICS_KEY = "silo.admin-nav.open-topics";

type TopicChoices = Record<string, boolean>;

function readChoices(): TopicChoices {
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(OPEN_TOPICS_KEY) ?? "{}");
    return parsed && typeof parsed === "object" ? (parsed as TopicChoices) : {};
  } catch {
    return {};
  }
}

function writeChoices(choices: TopicChoices) {
  try {
    localStorage.setItem(OPEN_TOPICS_KEY, JSON.stringify(choices));
  } catch {
    // Storage full or unavailable: the choice lasts for this page load.
  }
}

/** The choices with `topicId` open again, if the admin had closed it. */
function withTopicOpen(choices: TopicChoices, topicId: string | null): TopicChoices {
  return topicId && choices[topicId] === false ? { ...choices, [topicId]: true } : choices;
}

/**
 * Which admin sidebar topics are open. The topic holding the current page opens
 * by itself, including after the admin closed it on an earlier visit; every
 * other topic stays as the admin last left it, across navigations and reloads.
 */
export function useAdminNavTopics(activeTopicId: string | null) {
  const [choices, setChoices] = useState(() => withTopicOpen(readChoices(), activeTopicId));
  // Adjusted during render rather than in an effect, so arriving in a topic
  // never paints one frame with it closed.
  const [arrivedIn, setArrivedIn] = useState(activeTopicId);
  if (arrivedIn !== activeTopicId) {
    setArrivedIn(activeTopicId);
    const reopened = withTopicOpen(choices, activeTopicId);
    if (reopened !== choices) setChoices(reopened);
  }

  const isOpen = useCallback(
    (topicId: string) => choices[topicId] ?? topicId === activeTopicId,
    [activeTopicId, choices],
  );

  // The desktop sidebar and the phone drawer each run this hook, so a toggle
  // merges into what is stored instead of writing back this instance's whole
  // snapshot, which would undo a choice the other instance made since.
  const toggle = useCallback(
    (topicId: string) => {
      const open = !isOpen(topicId);
      writeChoices({ ...readChoices(), [topicId]: open });
      setChoices((current) => ({ ...current, [topicId]: open }));
    },
    [isOpen],
  );

  return { isOpen, toggle };
}
