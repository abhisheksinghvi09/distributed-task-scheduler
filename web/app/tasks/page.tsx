"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { listTasks, submitTask, searchTasksNL } from "@/lib/api-client";
import { Badge } from "@/components/ui/badge";
import { Button, buttonVariants } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
  DialogFooter,
} from "@/components/ui/dialog";

const STATUSES = ["", "pending", "queued", "running", "succeeded", "failed", "dead_letter", "blocked", "cancelled"];

function statusVariant(status: string): "default" | "secondary" | "destructive" | "outline" {
  if (status === "succeeded") return "default";
  if (status === "failed" || status === "dead_letter") return "destructive";
  if (status === "running" || status === "queued") return "secondary";
  return "outline";
}

export default function TasksPage() {
  const [status, setStatus] = useState("");
  const [cursors, setCursors] = useState<string[]>([""]);
  const queryClient = useQueryClient();
  const cursor = cursors[cursors.length - 1];

  // AI Search state
  const [aiSearchInput, setAiSearchInput] = useState("");
  const [activeAiQuery, setActiveAiQuery] = useState("");

  // Submit Task modal state
  const [submitOpen, setSubmitOpen] = useState(false);
  const [taskType, setTaskType] = useState<"noop" | "llm_task" | "http_request">("llm_task");
  const [llmPrompt, setLlmPrompt] = useState("Explain in one sentence why distributed task schedulers need idempotent workers.");
  const [httpUrl, setHttpUrl] = useState("https://httpbin.org/get");
  const [httpMethod, setHttpMethod] = useState("GET");
  const [priority, setPriority] = useState("0");
  const [maxAttempts, setMaxAttempts] = useState("3");

  // Standard tasks query
  const { data: standardData, isLoading: isStandardLoading, error: standardError } = useQuery({
    queryKey: ["tasks", status, cursor],
    queryFn: () => listTasks({ status: status || undefined, cursor: cursor || undefined }),
    enabled: !activeAiQuery,
  });

  // AI search query
  const { data: searchData, isLoading: isSearchLoading, error: searchError } = useQuery({
    queryKey: ["tasks-search", activeAiQuery],
    queryFn: () => searchTasksNL(activeAiQuery),
    enabled: Boolean(activeAiQuery),
  });

  const submitMutation = useMutation({
    mutationFn: async () => {
      let payload: Record<string, unknown> = {};
      if (taskType === "llm_task") {
        payload = { prompt: llmPrompt };
      } else if (taskType === "http_request") {
        payload = { url: httpUrl, method: httpMethod };
      }
      return submitTask({
        task_type: taskType,
        payload,
        priority: parseInt(priority, 10) || 0,
        max_attempts: parseInt(maxAttempts, 10) || 3,
      });
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["tasks"] });
      setSubmitOpen(false);
    },
  });

  const isAiActive = Boolean(activeAiQuery);
  const isLoading = isAiActive ? isSearchLoading : isStandardLoading;
  const error = isAiActive ? searchError : standardError;
  const tasks = isAiActive ? (searchData?.tasks ?? []) : (standardData?.tasks ?? []);
  const nextCursor = isAiActive ? searchData?.next_cursor : standardData?.next_cursor;


  return (
    <div className="space-y-6">
      {/* Header & Submit Action */}
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold">Tasks</h1>
          <p className="text-xs text-muted-foreground mt-0.5">
            Monitor, inspect, and dispatch background jobs across the distributed worker fleet
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => queryClient.invalidateQueries({ queryKey: isAiActive ? ["tasks-search"] : ["tasks"] })}
          >
            Refresh
          </Button>

          <Dialog open={submitOpen} onOpenChange={setSubmitOpen}>
            <DialogTrigger className={buttonVariants({ size: "sm" })}>Submit Task</DialogTrigger>
            <DialogContent className="sm:max-w-md">
              <DialogHeader>
                <DialogTitle>Submit New Task</DialogTitle>
              </DialogHeader>

              <div className="space-y-4 py-2">
                <div>
                  <label className="text-xs font-medium text-muted-foreground block mb-1">Task Type</label>
                  <Select
                    value={taskType}
                    onValueChange={(v) => {
                      if (v) setTaskType(v as "noop" | "llm_task" | "http_request");
                    }}
                  >
                    <SelectTrigger>
                      <SelectValue placeholder="Select type" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="llm_task">llm_task (Autonomous Gemini AI)</SelectItem>
                      <SelectItem value="noop">noop (Zero-op health test)</SelectItem>
                      <SelectItem value="http_request">http_request (Outbound HTTP)</SelectItem>
                    </SelectContent>
                  </Select>
                </div>

                {taskType === "llm_task" && (
                  <div className="space-y-1">
                    <label className="text-xs font-medium text-muted-foreground block">
                      AI Prompt (Executed by Gemini Orchestrator)
                    </label>
                    <textarea
                      rows={3}
                      value={llmPrompt}
                      onChange={(e) => setLlmPrompt(e.target.value)}
                      className="w-full rounded-md border border-input bg-background px-3 py-2 text-xs focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                      placeholder="Enter instruction for Gemini..."
                    />
                  </div>
                )}

                {taskType === "http_request" && (
                  <div className="space-y-2">
                    <div>
                      <label className="text-xs font-medium text-muted-foreground block mb-1">Method</label>
                      <Select
                        value={httpMethod}
                        onValueChange={(v) => {
                          if (v) setHttpMethod(v);
                        }}
                      >
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="GET">GET</SelectItem>
                          <SelectItem value="POST">POST</SelectItem>
                          <SelectItem value="PUT">PUT</SelectItem>
                          <SelectItem value="DELETE">DELETE</SelectItem>
                        </SelectContent>
                      </Select>
                    </div>
                    <div>
                      <label className="text-xs font-medium text-muted-foreground block mb-1">URL</label>
                      <Input
                        value={httpUrl}
                        onChange={(e) => setHttpUrl(e.target.value)}
                        placeholder="https://api.example.com/webhook"
                      />
                    </div>
                  </div>
                )}

                <div className="grid grid-cols-2 gap-3">
                  <div>
                    <label className="text-xs font-medium text-muted-foreground block mb-1">Priority (0-100)</label>
                    <Input
                      type="number"
                      value={priority}
                      onChange={(e) => setPriority(e.target.value)}
                    />
                  </div>
                  <div>
                    <label className="text-xs font-medium text-muted-foreground block mb-1">Max Attempts</label>
                    <Input
                      type="number"
                      value={maxAttempts}
                      onChange={(e) => setMaxAttempts(e.target.value)}
                    />
                  </div>
                </div>

                {submitMutation.error && (
                  <p className="text-xs text-destructive">{(submitMutation.error as Error).message}</p>
                )}
              </div>

              <DialogFooter>
                <Button
                  onClick={() => submitMutation.mutate()}
                  disabled={submitMutation.isPending}
                >
                  {submitMutation.isPending ? "Submitting…" : "Dispatch Task"}
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        </div>
      </div>

      {/* Filters: AI NL Search & Traditional Status Filter */}
      <div className="flex flex-col sm:flex-row gap-3">
        <div className="flex-1 flex gap-2">
          <Input
            placeholder="✨ AI Search in natural language (e.g. 'show failed tasks', 'llm tasks')..."
            value={aiSearchInput}
            onChange={(e) => setAiSearchInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && aiSearchInput.trim()) {
                e.preventDefault();
                setActiveAiQuery(aiSearchInput.trim());
              }
            }}
            className="text-xs"
          />
          <Button
            size="sm"
            variant="secondary"
            disabled={!aiSearchInput.trim() || isSearchLoading}
            onClick={() => setActiveAiQuery(aiSearchInput.trim())}
          >
            {isSearchLoading ? "Searching…" : "Search"}
          </Button>
          {isAiActive && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                setActiveAiQuery("");
                setAiSearchInput("");
              }}
            >
              Reset
            </Button>
          )}
        </div>

        {!isAiActive && (
          <Select
            value={status || "__all"}
            onValueChange={(v) => {
              setStatus(!v || v === "__all" ? "" : v);
              setCursors([""]);
            }}
          >
            <SelectTrigger className="w-full sm:w-44">
              <SelectValue placeholder="Filter by status" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="__all">All statuses</SelectItem>
              {STATUSES.filter(Boolean).map((s) => (
                <SelectItem key={s} value={s}>
                  {s}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </div>

      {/* Active AI Query Feedback */}
      {isAiActive && searchData && (
        <div className="text-xs bg-muted/60 p-2.5 rounded-md flex items-center gap-2">
          <span className="font-semibold text-primary">✨ AI Interpretation:</span>
          {searchData.interpreted_as && searchData.interpreted_as.length > 0 ? (
            <div className="flex flex-wrap gap-1.5">
              {searchData.interpreted_as.map((f, idx) => (
                <Badge key={idx} variant="outline" className="font-mono text-[11px]">
                  {f.column} {f.operator} {String(f.value)}
                </Badge>
              ))}
            </div>
          ) : (
            <span className="text-muted-foreground italic">No specific filters parsed</span>
          )}
        </div>
      )}

      {error && <p className="text-destructive text-sm">{(error as Error).message}</p>}
      {isLoading && <p className="text-muted-foreground text-sm">Loading tasks…</p>}

      {/* Tasks Table */}
      {!isLoading && (
        <>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>ID</TableHead>
                <TableHead>Type</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Attempts</TableHead>
                <TableHead>Scheduled at</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {tasks.map((t) => (
                <TableRow key={t.ID}>
                  <TableCell className="font-mono text-xs">
                    <Link href={`/tasks/${t.ID}`} className="hover:underline text-primary">
                      {t.ID.slice(0, 8)}…
                    </Link>
                  </TableCell>
                  <TableCell>
                    <span className="font-medium text-xs">{t.TaskType}</span>
                  </TableCell>
                  <TableCell>
                    <Badge variant={statusVariant(t.Status)}>{t.Status}</Badge>
                  </TableCell>
                  <TableCell className="text-xs">
                    {t.Attempts}/{t.MaxAttempts}
                  </TableCell>
                  <TableCell className="text-muted-foreground text-xs">
                    {new Date(t.ScheduledAt).toLocaleString()}
                  </TableCell>
                </TableRow>
              ))}
              {tasks.length === 0 && (
                <TableRow>
                  <TableCell colSpan={5} className="text-muted-foreground text-center py-6 text-sm">
                    No tasks found matching your criteria.
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>

          {!isAiActive && (
            <div className="flex justify-between items-center pt-2">
              <Button
                variant="outline"
                size="sm"
                disabled={cursors.length <= 1}
                onClick={() => setCursors((c) => c.slice(0, -1))}
              >
                Previous
              </Button>
              <span className="text-xs text-muted-foreground">
                Page {cursors.length}
              </span>
              <Button
                variant="outline"
                size="sm"
                disabled={!nextCursor}
                onClick={() => setCursors((c) => [...c, nextCursor!])}
              >
                Next
              </Button>
            </div>
          )}
        </>
      )}
    </div>
  );
}

