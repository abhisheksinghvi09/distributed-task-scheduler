"use client";

import { use } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { getTask, requeueTask, cancelTask } from "@/lib/api-client";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

export default function TaskDetailPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  const queryClient = useQueryClient();

  const { data: task, isLoading, error } = useQuery({
    queryKey: ["task", id],
    queryFn: () => getTask(id),
    refetchInterval: 3000,
  });

  if (isLoading) return <p className="text-muted-foreground text-sm">Loading…</p>;
  if (error) return <p className="text-destructive text-sm">{(error as Error).message}</p>;
  if (!task) return null;

  const isTerminal = ["succeeded", "failed", "dead_letter", "cancelled", "blocked"].includes(task.Status);

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold font-mono">{task.ID}</h1>
          <p className="text-muted-foreground text-sm">{task.TaskType}</p>
        </div>
        <Badge>{task.Status}</Badge>
      </div>

      <div className="flex gap-2">
        <Button
          variant="outline"
          size="sm"
          disabled={!isTerminal}
          onClick={async () => {
            await requeueTask(id);
            queryClient.invalidateQueries({ queryKey: ["task", id] });
          }}
        >
          Requeue
        </Button>
        <Button
          variant="destructive"
          size="sm"
          disabled={isTerminal}
          onClick={async () => {
            await cancelTask(id);
            queryClient.invalidateQueries({ queryKey: ["task", id] });
          }}
        >
          Cancel
        </Button>
      </div>

      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Attempts</CardTitle>
          </CardHeader>
          <CardContent>
            {task.Attempts} / {task.MaxAttempts}
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Scheduled at</CardTitle>
          </CardHeader>
          <CardContent>{new Date(task.ScheduledAt).toLocaleString()}</CardContent>
        </Card>
      </div>

      {task.LastError && (
        <Card>
          <CardHeader>
            <CardTitle className="text-sm text-destructive">Last error</CardTitle>
          </CardHeader>
          <CardContent>
            <pre className="whitespace-pre-wrap text-xs">{task.LastError}</pre>
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader>
          <CardTitle className="text-sm">Payload</CardTitle>
        </CardHeader>
        <CardContent>
          <pre className="whitespace-pre-wrap text-xs">{JSON.stringify(task.Payload, null, 2)}</pre>
        </CardContent>
      </Card>
    </div>
  );
}
