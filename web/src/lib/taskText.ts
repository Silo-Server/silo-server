import type { useServerText } from "@/i18n";

type ServerText = ReturnType<typeof useServerText>;

/**
 * A scheduled task's name or description in the interface language. Silo's own
 * tasks have a catalog entry keyed by the task's stable id
 * (`web.admin.tasks.task.<id>.name`, `-` written as `_`); a task without one,
 * such as a plugin's, shows the server's English as sent.
 */
export function taskText(
  serverText: ServerText,
  task: { key: string; name: string; description?: string },
  field: "name" | "description",
): string {
  const english = field === "name" ? task.name : (task.description ?? "");
  if (!/^[a-z0-9_-]+$/.test(task.key)) {
    return english;
  }
  return serverText(
    `web.admin.tasks.task.${task.key.replaceAll("-", "_")}.${field}`,
    undefined,
    english,
  );
}
