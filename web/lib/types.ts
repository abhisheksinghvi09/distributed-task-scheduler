import { z } from "zod";

// Mirrors internal/task.Task's JSON shape. Validated at the boundary --
// the one place runtime validation earns its weight, since this is the
// seam between a Go service's JSON and a TypeScript app that would
// otherwise just trust whatever shape arrives.
export const TaskSchema = z.object({
  ID: z.string(),
  Status: z.string(),
  TaskType: z.string(),
  Payload: z.unknown(),
  Attempts: z.number(),
  MaxAttempts: z.number(),
  Priority: z.number(),
  ScheduledAt: z.string(),
  LastError: z.string(),
  TenantID: z.string(),
  CreatedAt: z.string(),
  UpdatedAt: z.string(),
});
export type Task = z.infer<typeof TaskSchema>;

export const TaskListResponseSchema = z.object({
  tasks: z.array(TaskSchema).nullable(),
  next_cursor: z.string(),
});

export const ScheduleSchema = z.object({
  ID: z.string(),
  Name: z.string(),
  CronExpr: z.string(),
  Timezone: z.string(),
  TaskType: z.string(),
  Payload: z.unknown(),
  Priority: z.number(),
  MaxAttempts: z.number(),
  CatchupPolicy: z.string(),
  NextRunAt: z.string(),
  LastRunAt: z.string().nullable(),
  Paused: z.boolean(),
});
export type Schedule = z.infer<typeof ScheduleSchema>;

export const ScheduleListResponseSchema = z.object({
  schedules: z.array(ScheduleSchema).nullable(),
});

export const WorkerSchema = z.object({
  worker_id: z.string(),
  running_count: z.number(),
});
export const WorkerListResponseSchema = z.object({
  workers: z.array(WorkerSchema).nullable(),
});
export type Worker = z.infer<typeof WorkerSchema>;

export const StatsSchema = z.object({
  queue_depth: z.record(z.string(), z.number()),
  throughput_per_sec: z.number(),
  p95_duration_seconds: z.number(),
});
export type Stats = z.infer<typeof StatsSchema>;

export const CronSuggestionSchema = z.object({
  cron_expr: z.string(),
  timezone: z.string(),
  explanation: z.string(),
});
export type CronSuggestion = z.infer<typeof CronSuggestionSchema>;

export const TaskSearchFilterSchema = z.object({
  column: z.string(),
  operator: z.string(),
  value: z.unknown(),
});

export const TaskSearchResponseSchema = z.object({
  interpreted_as: z.array(TaskSearchFilterSchema).nullable(),
  tasks: z.array(TaskSchema).nullable(),
  next_cursor: z.string(),
});
export type TaskSearchResponse = z.infer<typeof TaskSearchResponseSchema>;

