// Idle connections: VUS sockets held open for HOLD seconds. Target: 10 000 per API instance.
//
//   k6 run -e VUS=10000 -e HOLD=300 loadtest/websockets.js
import ws from "k6/ws";
import { check } from "k6";
import { Counter } from "k6/metrics";
import { socketURL, userFor } from "./lib.js";

const VUS = Number(__ENV.VUS || 1000);
const HOLD = Number(__ENV.HOLD || 120);
const hellos = new Counter("ws_hello");
const closedEarly = new Counter("ws_closed_early");

export const options = {
  scenarios: {
    idle: { executor: "per-vu-iterations", vus: VUS, iterations: 1, maxDuration: `${HOLD + 120}s` },
  },
  thresholds: {
    ws_closed_early: ["count==0"],
    checks: ["rate>0.99"],
  },
};

export default function () {
  const u = userFor(__VU);
  const opened = Date.now();
  const res = ws.connect(socketURL(u), {}, (socket) => {
    socket.on("message", (raw) => {
      if (JSON.parse(raw).type === "hello") hellos.add(1);
    });
    socket.on("close", () => {
      if (Date.now() - opened < HOLD * 1000) closedEarly.add(1);
    });
    socket.setTimeout(() => socket.close(), HOLD * 1000);
  });
  check(res, { "upgraded (101)": (r) => r && r.status === 101 });
}
