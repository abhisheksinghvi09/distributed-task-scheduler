"use client";

import { useStatsStream } from "@/lib/use-stats-stream";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  LineChart,
  Line,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  AreaChart,
  Area,
} from "recharts";

const STATUS_COLORS: Record<string, string> = {
  pending: "#94a3b8",
  queued: "#60a5fa",
  running: "#fbbf24",
  succeeded: "#34d399",
  failed: "#f87171",
  dead_letter: "#991b1b",
  blocked: "#a78bfa",
  cancelled: "#64748b",
};

export function StatsPanel() {
  const { stats, history } = useStatsStream();

  const depthSeries = history.map((h) => ({
    t: new Date(h.t).toLocaleTimeString(),
    ...h.queue_depth,
  }));
  const throughputSeries = history.map((h) => ({
    t: new Date(h.t).toLocaleTimeString(),
    throughput: h.throughput_per_sec,
    p95: h.p95_duration_seconds,
  }));

  const statuses = Object.keys(stats?.queue_depth ?? {});

  return (
    <div className="grid gap-4 md:grid-cols-3">
      <Card>
        <CardHeader>
          <CardTitle className="text-sm font-medium text-muted-foreground">
            Throughput
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="text-2xl font-bold">
            {stats ? stats.throughput_per_sec.toFixed(2) : "—"}{" "}
            <span className="text-sm font-normal text-muted-foreground">tasks/sec</span>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-sm font-medium text-muted-foreground">
            p95 duration
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="text-2xl font-bold">
            {stats ? stats.p95_duration_seconds.toFixed(2) : "—"}{" "}
            <span className="text-sm font-normal text-muted-foreground">sec</span>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-sm font-medium text-muted-foreground">
            Queued + running
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="text-2xl font-bold">
            {stats ? (stats.queue_depth.queued ?? 0) + (stats.queue_depth.running ?? 0) : "—"}
          </div>
        </CardContent>
      </Card>

      <Card className="md:col-span-2">
        <CardHeader>
          <CardTitle className="text-sm font-medium text-muted-foreground">
            Queue depth by status
          </CardTitle>
        </CardHeader>
        <CardContent className="h-64">
          <ResponsiveContainer width="100%" height="100%">
            <AreaChart data={depthSeries}>
              <XAxis dataKey="t" hide />
              <YAxis width={30} />
              <Tooltip />
              {statuses.map((s) => (
                <Area
                  key={s}
                  type="monotone"
                  dataKey={s}
                  stackId="1"
                  stroke={STATUS_COLORS[s] ?? "#888"}
                  fill={STATUS_COLORS[s] ?? "#888"}
                  fillOpacity={0.5}
                />
              ))}
            </AreaChart>
          </ResponsiveContainer>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-sm font-medium text-muted-foreground">
            Throughput &amp; p95
          </CardTitle>
        </CardHeader>
        <CardContent className="h-64">
          <ResponsiveContainer width="100%" height="100%">
            <LineChart data={throughputSeries}>
              <XAxis dataKey="t" hide />
              <YAxis width={30} />
              <Tooltip />
              <Line type="monotone" dataKey="throughput" stroke="#34d399" dot={false} />
              <Line type="monotone" dataKey="p95" stroke="#f87171" dot={false} />
            </LineChart>
          </ResponsiveContainer>
        </CardContent>
      </Card>
    </div>
  );
}
