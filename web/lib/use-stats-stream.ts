"use client";

import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Stats, StatsSchema } from "./types";

// Live stats via native EventSource against the same-origin BFF route --
// the browser never talks to the Go backend, so no Authorization header
// is needed (EventSource can't set one anyway) and no CORS config exists
// to get wrong.
//
// Deliberately does NOT stream the task list itself: on each tick this
// just invalidates the "tasks" query key and lets TanStack Query refetch.
// Streaming table rows is how these dashboards turn into unmaintainable
// state machines -- the stats panel is the only thing that needs to be
// truly live.
export function useStatsStream() {
  const [stats, setStats] = useState<Stats | null>(null);
  const [history, setHistory] = useState<(Stats & { t: number })[]>([]);
  const queryClient = useQueryClient();

  useEffect(() => {
    const source = new EventSource("/api/stream");

    source.onmessage = (event) => {
      try {
        const parsed = StatsSchema.parse(JSON.parse(event.data));
        setStats(parsed);
        setHistory((prev) => [...prev.slice(-59), { ...parsed, t: Date.now() }]);
        queryClient.invalidateQueries({ queryKey: ["tasks"] });
        queryClient.invalidateQueries({ queryKey: ["workers"] });
      } catch {
        // Malformed frame: ignore it and wait for the next tick rather
        // than tearing down the whole connection over one bad payload.
      }
    };

    return () => source.close();
  }, [queryClient]);

  return { stats, history };
}
