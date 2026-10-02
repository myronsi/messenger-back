import ipaddress
import threading
import time
from collections import deque

from fastapi import HTTPException, Request


class SlidingWindowLimiter:
    """In-memory sliding window counter. State is per process."""

    MAX_KEYS = 10000

    def __init__(self, limit: int, window_seconds: int, clock=time.monotonic):
        self.limit = limit
        self.window = window_seconds
        self._clock = clock
        self._hits: dict[str, deque] = {}
        self._lock = threading.Lock()

    def _prune(self, now: float, events: deque) -> None:
        while events and events[0] <= now - self.window:
            events.popleft()

    def retry_after(self, key: str) -> int:
        """Seconds until the key may try again, or 0 when it is not limited."""
        now = self._clock()
        with self._lock:
            events = self._hits.get(key)
            if not events:
                return 0
            self._prune(now, events)
            if not events:
                del self._hits[key]
                return 0
            if len(events) < self.limit:
                return 0
            return max(1, int(events[0] + self.window - now) + 1)

    def hit(self, key: str) -> None:
        now = self._clock()
        with self._lock:
            if len(self._hits) >= self.MAX_KEYS and key not in self._hits:
                for stale_key in list(self._hits):
                    events = self._hits[stale_key]
                    self._prune(now, events)
                    if not events:
                        del self._hits[stale_key]
                if len(self._hits) >= self.MAX_KEYS:
                    self._hits.pop(next(iter(self._hits)))
            self._hits.setdefault(key, deque()).append(now)

    def reset(self, key: str) -> None:
        with self._lock:
            self._hits.pop(key, None)

    def clear(self) -> None:
        with self._lock:
            self._hits.clear()


_PROXY_NETWORKS = tuple(
    ipaddress.ip_network(cidr) for cidr in ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7")
)


def _is_trusted_proxy(host: str | None) -> bool:
    if not host:
        return False
    try:
        address = ipaddress.ip_address(host)
    except ValueError:
        return False
    return address.is_loopback or any(address in network for network in _PROXY_NETWORKS)


def client_ip(request: Request) -> str:
    """Real client address; forwarding headers are only honoured from a private (proxy) peer."""
    peer = request.client.host if request.client else None
    if _is_trusted_proxy(peer):
        real_ip = request.headers.get("x-real-ip", "").strip()
        if real_ip:
            return real_ip
        forwarded = request.headers.get("x-forwarded-for", "")
        if forwarded:
            last = forwarded.split(",")[-1].strip()
            if last:
                return last
    return peer or "unknown"


def enforce(limiter: SlidingWindowLimiter, *keys: str) -> None:
    wait = max((limiter.retry_after(key) for key in keys), default=0)
    if wait:
        raise HTTPException(
            status_code=429,
            detail="Too many attempts. Try again later.",
            headers={"Retry-After": str(wait)},
        )


def record(limiter: SlidingWindowLimiter, *keys: str) -> None:
    for key in keys:
        limiter.hit(key)


LOGIN_IP = SlidingWindowLimiter(limit=20, window_seconds=15 * 60)
LOGIN_USER = SlidingWindowLimiter(limit=8, window_seconds=15 * 60)
TWO_FACTOR_IP = SlidingWindowLimiter(limit=20, window_seconds=15 * 60)
TWO_FACTOR_USER = SlidingWindowLimiter(limit=10, window_seconds=15 * 60)
RECOVER_IP = SlidingWindowLimiter(limit=10, window_seconds=60 * 60)
RECOVER_USER = SlidingWindowLimiter(limit=5, window_seconds=60 * 60)
RESET_IP = SlidingWindowLimiter(limit=20, window_seconds=60 * 60)

ALL_LIMITERS = (LOGIN_IP, LOGIN_USER, TWO_FACTOR_IP, TWO_FACTOR_USER, RECOVER_IP, RECOVER_USER, RESET_IP)


def clear_all() -> None:
    for limiter in ALL_LIMITERS:
        limiter.clear()