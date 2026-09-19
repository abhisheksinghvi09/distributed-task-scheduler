// The BFF proxy: the browser never talks to the Go API directly.
//
// This is the whole reason a separately-deployed dashboard doesn't need
// CORS, doesn't leak an API key into the bundle, and can still use native
// EventSource for the live stats stream (which cannot set an Authorization
// header -- a hard limitation of the browser API, not a config issue).
// Every request from the browser hits this same-origin route, which
// injects the server-only API key and forwards to the real backend.
//
// TASK_API_URL / TASK_API_KEY are server-only env vars -- never
// NEXT_PUBLIC_*, which would ship them into the client bundle.
import { NextRequest } from "next/server";

const BACKEND_URL = process.env.TASK_API_URL ?? "http://localhost:18081";
const API_KEY = process.env.TASK_API_KEY ?? "";

async function proxy(req: NextRequest, path: string[]) {
  const targetPath = path.join("/");
  const search = req.nextUrl.search;
  const targetUrl = `${BACKEND_URL}/v1/${targetPath}${search}`;

  const headers = new Headers();
  headers.set("Authorization", `Bearer ${API_KEY}`);
  const contentType = req.headers.get("content-type");
  if (contentType) headers.set("Content-Type", contentType);

  const init: RequestInit = {
    method: req.method,
    headers,
    // @ts-expect-error -- Node's fetch supports duplex for streamed bodies;
    // required when forwarding a request with a body.
    duplex: "half",
  };
  if (req.method !== "GET" && req.method !== "HEAD") {
    init.body = await req.arrayBuffer();
  }

  const upstream = await fetch(targetUrl, init);

  // The SSE stream is forwarded as-is: the browser's EventSource hits this
  // same-origin route, which pipes the upstream response body straight
  // through, headers included.
  return new Response(upstream.body, {
    status: upstream.status,
    headers: upstream.headers,
  });
}

export async function GET(req: NextRequest, { params }: { params: Promise<{ path: string[] }> }) {
  return proxy(req, (await params).path);
}
export async function POST(req: NextRequest, { params }: { params: Promise<{ path: string[] }> }) {
  return proxy(req, (await params).path);
}
export async function PATCH(req: NextRequest, { params }: { params: Promise<{ path: string[] }> }) {
  return proxy(req, (await params).path);
}
export async function DELETE(req: NextRequest, { params }: { params: Promise<{ path: string[] }> }) {
  return proxy(req, (await params).path);
}
