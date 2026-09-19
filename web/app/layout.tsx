import { ClerkProvider, SignInButton, SignUpButton, Show, UserButton } from "@clerk/nextjs";
import { shadcn } from "@clerk/ui/themes";
import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import Link from "next/link";
import "./globals.css";
import { Providers } from "./providers";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

export const metadata: Metadata = {
  title: "AutCron",
  description: "AutCron - Distributed Task Scheduler & Autonomous AI Orchestration",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html
      lang="en"
      className={`${geistSans.variable} ${geistMono.variable} h-full antialiased`}
    >
      <body className="min-h-full flex flex-col bg-background text-foreground">
        <ClerkProvider appearance={{ theme: shadcn }}>
          <Providers>
            <header className="border-b">
              <nav className="mx-auto max-w-6xl flex items-center justify-between px-4 py-3 text-sm">
                <div className="flex items-center gap-6">
                  <Link href="/" className="font-bold text-base tracking-tight hover:opacity-90">
                    AutCron
                  </Link>
                  <Link href="/tasks" className="hover:underline">
                    Tasks
                  </Link>
                  <Link href="/schedules" className="hover:underline">
                    Schedules
                  </Link>
                  <Link href="/workers" className="hover:underline">
                    Workers
                  </Link>
                </div>
                <div className="flex items-center gap-3">
                  <Show when="signed-out">
                    <SignInButton mode="modal">
                      <button className="px-3 py-1.5 text-xs font-medium rounded-md hover:bg-muted transition-colors cursor-pointer">
                        Sign In
                      </button>
                    </SignInButton>
                    <SignUpButton mode="modal">
                      <button className="px-3 py-1.5 text-xs font-medium bg-primary text-primary-foreground rounded-md hover:bg-primary/90 transition-colors cursor-pointer">
                        Sign Up
                      </button>
                    </SignUpButton>
                  </Show>
                  <Show when="signed-in">
                    <UserButton />
                  </Show>
                </div>
              </nav>
            </header>
            <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-6">{children}</main>
          </Providers>
        </ClerkProvider>
      </body>
    </html>
  );
}