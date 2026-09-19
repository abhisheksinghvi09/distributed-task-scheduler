import Link from "next/link";
import { StatsPanel } from "@/components/stats-panel";
import { Badge } from "@/components/ui/badge";
import { buttonVariants } from "@/components/ui/button";

export default function Home() {
  return (
    <div className="space-y-8">
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4 pb-2 border-b">
        <div>
          <div className="flex items-center gap-2">
            <h1 className="text-2xl font-bold tracking-tight">AutCron Platform</h1>
            <Badge variant="default" className="text-[10px] bg-emerald-600">
              Live
            </Badge>
          </div>
          <p className="text-sm text-muted-foreground mt-1">
            Autonomous distributed task scheduling, cron management, and AI execution fleet.
          </p>
          <div className="flex flex-wrap gap-2 mt-3 text-xs">
            <span className="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full bg-muted font-medium">
              <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse"></span>
              Distributed Workers: 3 Active
            </span>
            <span className="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full bg-muted font-medium">
              ✨ AI Model: Gemini 3.5 Flash-Lite
            </span>
            <span className="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full bg-muted font-medium">
              🔐 Auth: Clerk
            </span>
          </div>
        </div>

        <div className="flex items-center gap-2">
          <Link href="/tasks" className={buttonVariants({ size: "sm" })}>
            View Tasks
          </Link>
          <Link href="/schedules" className={buttonVariants({ variant: "outline", size: "sm" })}>
            Schedules
          </Link>
        </div>
      </div>

      <div className="space-y-4">
        <h2 className="text-lg font-semibold tracking-tight">System Telemetry & Queue Depth</h2>
        <StatsPanel />
      </div>
    </div>
  );
}

