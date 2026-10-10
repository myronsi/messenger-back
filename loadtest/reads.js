// History pages and searches under load. Targets: p99 of a history page under 50 ms, of a search under 300 ms.
// Each request goes out as a random seeded user: one user may search 60 times a minute (bursts of 20).
// Run messages.js first (or against migrated data) so there is history to read and find.
//
//   k6 run -e RATE=200 -e DURATION=120 loadtest/reads.js
import http from "k6/http";
import { check } from "k6";
import { BASE, anyUser, headers } from "./lib.js";

const RATE = Number(__ENV.RATE || 200);
const DURATION = Number(__ENV.DURATION || 120);
const WORDS = (__ENV.WORDS || "hello,meeting,report,photo,tomorrow").split(",");

export const options = {
  scenarios: {
    history: {
      executor: "constant-arrival-rate", rate: RATE, timeUnit: "1s", duration: `${DURATION}s`,
      preAllocatedVUs: 50, maxVUs: 500, exec: "history",
    },
    search: {
      executor: "constant-arrival-rate", rate: Math.max(1, Math.round(RATE / 10)), timeUnit: "1s", duration: `${DURATION}s`,
      preAllocatedVUs: 20, maxVUs: 200, exec: "search",
    },
  },
  thresholds: {
    "http_req_duration{scenario:history}": ["p(99)<50"],
    "http_req_duration{scenario:search}": ["p(99)<300"],
    http_req_failed: ["rate<0.01"],
  },
};

export function history() {
  const u = anyUser();
  const res = http.get(`${BASE}/chats/${u.chat_id}/messages?limit=50`, headers(u));
  check(res, { "history 200": (r) => r.status === 200 });
}

export function search() {
  const u = anyUser();
  const q = WORDS[Math.floor(Math.random() * WORDS.length)];
  const res = http.get(`${BASE}/search/messages?q=${q}`, headers(u));
  check(res, { "search 200": (r) => r.status === 200 });
}
