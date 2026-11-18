/**
 * use-llm-managed.ts: hook that says whether model access is managed outside ARTEX.
 * Main functions/types:
 *   - useLlmManaged: true on hosted agents, where the owner connects the model on the agent's own
 *     page and ARTEX's LLM settings are locked. The answer is fetched once and shared.
 */
"use client";

import { useEffect, useState } from "react";

import { api } from "@/lib/api";

let cached: Promise<boolean> | null = null;

function load(): Promise<boolean> {
  if (!cached) {
    cached = api
      .stats()
      .then((stats) => stats.llm_managed === true)
      .catch(() => {
        cached = null; // not signed in yet, or offline: ask again next time
        return false;
      });
  }
  return cached;
}

export function useLlmManaged(): boolean {
  const [managed, setManaged] = useState(false);

  useEffect(() => {
    let alive = true;
    void load().then((value) => {
      if (alive) setManaged(value);
    });
    return () => {
      alive = false;
    };
  }, []);

  return managed;
}
