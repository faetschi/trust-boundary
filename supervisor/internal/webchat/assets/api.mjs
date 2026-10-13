// api.js — the only transport to the gateway. GET-only; the client never sends
// a body and never targets a control route, because none exists.

export async function fetchSnapshot(signal) {
  const response = await fetch("/api/v1/snapshot", {
    method: "GET",
    headers: { Accept: "application/json" },
    cache: "no-store",
    signal,
  });
  if (!response.ok) {
    throw new Error(`snapshot request failed (${response.status})`);
  }
  return response.json();
}

export function openEventStream({ cursor, onItem, onSession, onGap, onConnection, onError }) {
  const url = cursor ? `/api/v1/events?cursor=${encodeURIComponent(cursor)}` : "/api/v1/events";
  const source = new EventSource(url);
  const decode = (handler) => (event) => {
    try {
      handler(JSON.parse(event.data));
    } catch (error) {
      if (onError) onError(error);
    }
  };
  source.addEventListener("item", decode(onItem));
  source.addEventListener("session", decode(onSession));
  source.addEventListener("gap", decode(onGap));
  source.addEventListener("open", () => onConnection && onConnection("open"));
  source.addEventListener("error", () => onConnection && onConnection("reconnecting"));
  return {
    close() {
      source.close();
    },
  };
}
