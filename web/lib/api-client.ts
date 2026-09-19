// Every call here hits the same-origin BFF proxy (/api/...), never the Go
// backend directly -- see app/api/[...path]/route.ts for why.
import {
  Task,
  TaskListResponseSchema,
  TaskSchema,
  Schedule,
  ScheduleListResponseSchema,
  ScheduleSchema,
  WorkerListResponseSchema,
  StatsSchema,
  CronSuggestion,
  CronSuggestionSchema,
  TaskSearchResponse,
  TaskSearchResponseSchema,
} from "./types";

async function apiFetch(path: string, init?: RequestInit) {
  const res = await fetch(`/api/${path}`, {
    ...init,
    headers: { "Content-Type": "application/json", ...init?.headers },
  });
  if (!res.ok) {
    const body = await res.text();
    throw new Error(`${res.status} ${res.statusText}: ${body}`);
  }
  return res;
}

export interface ListTasksParams {
  status?: string;
  task_type?: string;
  cursor?: string;
  limit?: number;
}

export async function listTasks(params: ListTasksParams = {}) {
  const qs = new URLSearchParams();
  if (params.status) qs.set("status", params.status);
  if (params.task_type) qs.set("task_type", params.task_type);
  if (params.cursor) qs.set("cursor", params.cursor);
  if (params.limit) qs.set("limit", String(params.limit));

  const res = await apiFetch(`tasks?${qs.toString()}`);
  return TaskListResponseSchema.parse(await res.json());
}

export async function getTask(id: string): Promise<Task> {
  const res = await apiFetch(`tasks/${id}`);
  return TaskSchema.parse(await res.json());
}

export async function requeueTask(id: string) {
  await apiFetch(`tasks/${id}/requeue`, { method: "POST" });
}

export async function cancelTask(id: string) {
  await apiFetch(`tasks/${id}/cancel`, { method: "POST" });
}

export async function submitTask(input: {
  task_type: string;
  payload?: unknown;
  priority?: number;
  max_attempts?: number;
}) {
  const res = await apiFetch("tasks", { method: "POST", body: JSON.stringify(input) });
  return res.json();
}

export async function listSchedules(): Promise<Schedule[]> {
  const res = await apiFetch("schedules");
  return ScheduleListResponseSchema.parse(await res.json()).schedules ?? [];
}

export async function getSchedule(id: string): Promise<Schedule> {
  const res = await apiFetch(`schedules/${id}`);
  return ScheduleSchema.parse(await res.json());
}

export async function createSchedule(input: {
  name: string;
  cron_expr: string;
  timezone?: string;
  task_type: string;
  payload?: unknown;
  catchup_policy?: string;
}) {
  await apiFetch("schedules", { method: "POST", body: JSON.stringify(input) });
}

export async function setSchedulePaused(id: string, paused: boolean) {
  await apiFetch(`schedules/${id}`, { method: "PATCH", body: JSON.stringify({ paused }) });
}

export async function deleteSchedule(id: string) {
  await apiFetch(`schedules/${id}`, { method: "DELETE" });
}

export async function triggerSchedule(id: string) {
  await apiFetch(`schedules/${id}/trigger`, { method: "POST" });
}

export async function listWorkers() {
  const res = await apiFetch("workers");
  return WorkerListResponseSchema.parse(await res.json()).workers ?? [];
}

export async function getStats() {
  const res = await apiFetch("stats");
  return StatsSchema.parse(await res.json());
}

export async function suggestCron(description: string): Promise<CronSuggestion> {
  const res = await apiFetch("schedules/suggest-cron", {
    method: "POST",
    body: JSON.stringify({ description }),
  });
  return CronSuggestionSchema.parse(await res.json());
}

export async function searchTasksNL(query: string, cursor?: string): Promise<TaskSearchResponse> {
  const qs = cursor ? `?cursor=${encodeURIComponent(cursor)}` : "";
  const res = await apiFetch(`tasks/search${qs}`, {
    method: "POST",
    body: JSON.stringify({ query }),
  });
  return TaskSearchResponseSchema.parse(await res.json());
}

