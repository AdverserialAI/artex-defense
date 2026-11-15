import { get, getToken, post } from "@/lib/api/http";
import type { DefenseBundle, DefenseTask, DefenseTaskListItem } from "@/lib/defense";

export const defenseApi = {
  defenseTasks: () => get<DefenseTaskListItem[]>("/defense/tasks"),
  defenseTask: (id: string) => get<DefenseBundle>(`/defense/tasks/${encodeURIComponent(id)}`),
  createDefenseTask: (task: DefenseTask, mode: "audit" | "run") =>
    post<DefenseBundle>("/defense/tasks", { task, mode }),
  approveDefenseTask: (id: string) =>
    post<DefenseBundle>(`/defense/tasks/${encodeURIComponent(id)}/approve`, { confirm: true }),
  downloadDefenseArtifact: async (
    id: string,
    file: "bundle.json" | "patch.diff" | "remediation.md" | "validation.json" | "findings.json",
  ) => {
    const token = getToken();
    const response = await fetch(
      `/api/defense/tasks/${encodeURIComponent(id)}/export?file=${encodeURIComponent(file)}`,
      {
        headers: token ? { Authorization: `Bearer ${token}` } : undefined,
      },
    );
    if (!response.ok) {
      const body = (await response.json().catch(() => null)) as { error?: string } | null;
      throw new Error(body?.error ?? `Export failed: ${response.status}`);
    }
    const blob = await response.blob();
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = file;
    anchor.click();
    URL.revokeObjectURL(url);
  },
};

export type DefenseApi = typeof defenseApi;
