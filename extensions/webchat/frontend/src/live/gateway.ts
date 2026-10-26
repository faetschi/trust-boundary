// gateway.ts — which gateway this page was served by. A live gateway answers GET /api/v1/contract with "mode": "live", and
// answers 401 until the browser is paired. A demonstration gateway (fixture, replay, receipt) answers a descriptor with no
// mode, and so does anything this page cannot read: the demonstration path then shows its own error states.

import type { LiveFetch } from "./client";
import { parseGatewayKind } from "./wire";

export type GatewayMode = "demo" | "live" | "unpaired";

export async function detectGateway(fetch: LiveFetch): Promise<GatewayMode> {
  try {
    const response = await fetch("/api/v1/contract", {
      method: "GET",
      headers: { Accept: "application/json" },
      credentials: "same-origin",
      cache: "no-store",
    });
    if (response.status === 401) return "unpaired";
    if (!response.ok) return "demo";
    return parseGatewayKind(await response.json());
  } catch {
    return "demo";
  }
}
