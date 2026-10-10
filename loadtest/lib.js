// Shared helpers of the k6 load tests (docs/load-testing.md).
import http from "k6/http";
import { SharedArray } from "k6/data";

export const BASE = __ENV.BASE_URL || "http://localhost:8080/api/v2";
export const WS_BASE = BASE.replace(/^http/, "ws");
export const API_VERSION = __ENV.API_VERSION || "2.0.0-alpha.8";

// The users written by `go run ./loadtest/seed`: users 2k and 2k+1 share a direct chat.
export const users = new SharedArray("users", () => JSON.parse(open(__ENV.USERS || "./users.json")));

export function headers(u) {
  return {
    headers: {
      Authorization: `Bearer ${u.access_token}`,
      "Content-Type": "application/json",
      "X-Client-Api-Version": API_VERSION,
    },
  };
}

// userFor spreads the virtual users over the seeded users.
export function userFor(vu) {
  return users[(vu - 1) % users.length];
}

// socketURL redeems a fresh one-time ticket for the user's WebSocket.
export function socketURL(u) {
  const res = http.post(`${BASE}/ws/ticket`, null, headers(u));
  if (res.status !== 200) {
    throw new Error(`ticket: ${res.status} ${res.body}`);
  }
  return `${WS_BASE}/ws?ticket=${encodeURIComponent(res.json("ticket"))}&api_version=${API_VERSION}`;
}
