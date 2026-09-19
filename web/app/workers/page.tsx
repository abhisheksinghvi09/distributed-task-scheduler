"use client";

import { useQuery } from "@tanstack/react-query";
import { listWorkers } from "@/lib/api-client";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

export default function WorkersPage() {
  const { data: workers, isLoading, error } = useQuery({
    queryKey: ["workers"],
    queryFn: listWorkers,
    refetchInterval: 5000,
  });

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold">Workers</h1>
      <p className="text-muted-foreground text-sm">
        Derived from currently running tasks -- there is no separate worker registry. A
        worker with zero running tasks simply doesn&apos;t appear here.
      </p>

      {error && <p className="text-destructive text-sm">{(error as Error).message}</p>}
      {isLoading && <p className="text-muted-foreground text-sm">Loading…</p>}

      <div className="grid gap-4 md:grid-cols-3">
        {(workers ?? []).map((w) => (
          <Card key={w.worker_id}>
            <CardHeader>
              <CardTitle className="font-mono text-sm">{w.worker_id}</CardTitle>
            </CardHeader>
            <CardContent>
              <div className="text-2xl font-bold">{w.running_count}</div>
              <div className="text-muted-foreground text-xs">running tasks</div>
            </CardContent>
          </Card>
        ))}
        {(workers ?? []).length === 0 && !isLoading && (
          <p className="text-muted-foreground text-sm">No workers currently running a task.</p>
        )}
      </div>
    </div>
  );
}
