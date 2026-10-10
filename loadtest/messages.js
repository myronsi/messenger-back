// Message delivery: every virtual user holds a socket and sends to its chat partner at RATE messages per
// second in total; the partner measures the time from send to delivery. Target: p99 under 150 ms at 500/s.
//
//   k6 run -e VUS=400 -e RATE=500 -e DURATION=120 loadtest/messages.js
//
// The send limit of one user is 30 per 10 s (bursts of 20), so RATE / VUS must stay below 3.
import ws from "k6/ws";
import { check } from "k6";
import { Counter, Trend } from "k6/metrics";
import { socketURL, userFor } from "./lib.js";

const VUS = Number(__ENV.VUS || 400);
const RATE = Number(__ENV.RATE || 500);
const DURATION = Number(__ENV.DURATION || 120);
const delivery = new Trend("message_delivery_ms", true);
const acks = new Trend("message_ack_ms", true);
const errors = new Counter("message_errors");

export const options = {
  scenarios: {
    chat: { executor: "per-vu-iterations", vus: VUS, iterations: 1, maxDuration: `${DURATION + 60}s` },
  },
  thresholds: {
    message_delivery_ms: ["p(99)<150"],
    message_errors: ["count<10"],
  },
};

export default function () {
  const u = userFor(__VU);
  const interval = (1000 * VUS) / RATE; // ms between this user's sends
  const sentAt = {};
  let n = 0;
  const res = ws.connect(socketURL(u), {}, (socket) => {
    socket.on("message", (raw) => {
      const ev = JSON.parse(raw);
      if (ev.type === "ack" && sentAt[ev.client_temp_id]) {
        acks.add(Date.now() - sentAt[ev.client_temp_id]);
      } else if (ev.type === "error") {
        errors.add(1);
      } else if (ev.type === "message") {
        const m = ev.data.message;
        // The partner's messages carry their send time.
        if (m.sender && m.sender.id !== u.id && m.content && m.content.startsWith("t=")) {
          delivery.add(Date.now() - Number(m.content.slice(2)));
        }
      }
    });
    socket.setInterval(() => {
      const temp = `${__VU}-${n++}`;
      sentAt[temp] = Date.now();
      socket.send(JSON.stringify({ type: "message", client_temp_id: temp, chat_id: u.chat_id, data: { type: "text", content: `t=${Date.now()}` } }));
    }, interval);
    socket.setTimeout(() => socket.close(), DURATION * 1000);
  });
  check(res, { "upgraded (101)": (r) => r && r.status === 101 });
}
