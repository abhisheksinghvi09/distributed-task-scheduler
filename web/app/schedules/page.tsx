"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  listSchedules,
  setSchedulePaused,
  triggerSchedule,
  createSchedule,
  deleteSchedule,
  suggestCron,
} from "@/lib/api-client";
import { Button, buttonVariants } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger, DialogFooter } from "@/components/ui/dialog";

export default function SchedulesPage() {
  const queryClient = useQueryClient();
  const { data: schedules, isLoading, error } = useQuery({
    queryKey: ["schedules"],
    queryFn: listSchedules,
    refetchInterval: 5000,
  });

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ["schedules"] });

  const pauseMutation = useMutation({
    mutationFn: ({ id, paused }: { id: string; paused: boolean }) => setSchedulePaused(id, paused),
    onSuccess: invalidate,
  });
  const triggerMutation = useMutation({ mutationFn: triggerSchedule, onSuccess: invalidate });
  const deleteMutation = useMutation({ mutationFn: deleteSchedule, onSuccess: invalidate });

  const [open, setOpen] = useState(false);
  const [aiPrompt, setAiPrompt] = useState("");
  const [aiExplanation, setAiExplanation] = useState("");
  const [form, setForm] = useState({ name: "", cron_expr: "", task_type: "noop" });

  const suggestMutation = useMutation({
    mutationFn: (desc: string) => suggestCron(desc),
    onSuccess: (data) => {
      setForm((prev) => ({ ...prev, cron_expr: data.cron_expr }));
      setAiExplanation(data.explanation);
    },
  });

  const createMutation = useMutation({
    mutationFn: () => createSchedule(form),
    onSuccess: () => {
      invalidate();
      setOpen(false);
      setForm({ name: "", cron_expr: "", task_type: "noop" });
      setAiPrompt("");
      setAiExplanation("");
    },
  });

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">Schedules</h1>
          <p className="text-xs text-muted-foreground mt-0.5">Automate recurring tasks with standard cron or natural language AI</p>
        </div>
        <Dialog open={open} onOpenChange={setOpen}>
          <DialogTrigger className={buttonVariants({ size: "sm" })}>New schedule</DialogTrigger>
          <DialogContent className="sm:max-w-md">
            <DialogHeader>
              <DialogTitle>Create schedule</DialogTitle>
            </DialogHeader>
            <div className="space-y-4 py-2">
              <div className="rounded-lg border border-primary/20 bg-primary/5 p-3 space-y-2">
                <label className="text-xs font-semibold text-primary flex items-center gap-1.5">
                  ✨ AI Cron Generator (Powered by Gemini)
                </label>
                <div className="flex gap-2">
                  <Input
                    placeholder="e.g. every Monday at 9am UTC"
                    value={aiPrompt}
                    onChange={(e) => setAiPrompt(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" && aiPrompt.trim()) {
                        e.preventDefault();
                        suggestMutation.mutate(aiPrompt.trim());
                      }
                    }}
                    className="text-xs"
                  />
                  <Button
                    type="button"
                    size="sm"
                    variant="secondary"
                    disabled={!aiPrompt.trim() || suggestMutation.isPending}
                    onClick={() => suggestMutation.mutate(aiPrompt.trim())}
                  >
                    {suggestMutation.isPending ? "Thinking…" : "Suggest"}
                  </Button>
                </div>
                {aiExplanation && (
                  <p className="text-xs text-muted-foreground italic bg-background/50 p-2 rounded">
                    &ldquo;{aiExplanation}&rdquo;
                  </p>
                )}
                {suggestMutation.error && (
                  <p className="text-xs text-destructive">{(suggestMutation.error as Error).message}</p>
                )}
              </div>

              <div className="space-y-3">
                <div>
                  <label className="text-xs font-medium text-muted-foreground block mb-1">Schedule Name</label>
                  <Input
                    placeholder="e.g. daily-database-cleanup"
                    value={form.name}
                    onChange={(e) => setForm({ ...form, name: e.target.value })}
                  />
                </div>
                <div>
                  <label className="text-xs font-medium text-muted-foreground block mb-1">Cron Expression</label>
                  <Input
                    placeholder="e.g. 0 9 * * 1"
                    value={form.cron_expr}
                    onChange={(e) => setForm({ ...form, cron_expr: e.target.value })}
                  />
                </div>
                <div>
                  <label className="text-xs font-medium text-muted-foreground block mb-1">Task Type</label>
                  <Select
                    value={form.task_type}
                    onValueChange={(val) => setForm({ ...form, task_type: val ?? "noop" })}
                  >
                    <SelectTrigger>
                      <SelectValue placeholder="Select type" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="noop">noop (diagnostic smoke test)</SelectItem>
                      <SelectItem value="llm_task">llm_task (Gemini AI prompt)</SelectItem>
                      <SelectItem value="http_request">http_request (Webhook / REST API)</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
              </div>
            </div>
            <DialogFooter>
              <Button onClick={() => createMutation.mutate()} disabled={!form.name || !form.cron_expr || createMutation.isPending}>
                {createMutation.isPending ? "Creating…" : "Create"}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>

      {error && <p className="text-destructive text-sm">{(error as Error).message}</p>}
      {isLoading && <p className="text-muted-foreground text-sm">Loading…</p>}

      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Name</TableHead>
            <TableHead>Cron</TableHead>
            <TableHead>Task type</TableHead>
            <TableHead>Next run</TableHead>
            <TableHead>State</TableHead>
            <TableHead>Actions</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(schedules ?? []).map((s) => (
            <TableRow key={s.ID}>
              <TableCell>{s.Name}</TableCell>
              <TableCell className="font-mono text-xs">{s.CronExpr}</TableCell>
              <TableCell>{s.TaskType}</TableCell>
              <TableCell className="text-muted-foreground text-xs">
                {new Date(s.NextRunAt).toLocaleString()}
              </TableCell>
              <TableCell>
                <Badge variant={s.Paused ? "outline" : "default"}>{s.Paused ? "paused" : "active"}</Badge>
              </TableCell>
              <TableCell className="space-x-2">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => pauseMutation.mutate({ id: s.ID, paused: !s.Paused })}
                >
                  {s.Paused ? "Resume" : "Pause"}
                </Button>
                <Button variant="outline" size="sm" onClick={() => triggerMutation.mutate(s.ID)}>
                  Run now
                </Button>
                <Button variant="destructive" size="sm" onClick={() => deleteMutation.mutate(s.ID)}>
                  Delete
                </Button>
              </TableCell>
            </TableRow>
          ))}
          {(schedules ?? []).length === 0 && (
            <TableRow>
              <TableCell colSpan={6} className="text-muted-foreground text-center">
                No schedules yet.
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
    </div>
  );
}
