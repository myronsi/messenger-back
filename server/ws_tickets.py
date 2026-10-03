"""Short-lived, single-use tickets that authenticate a WebSocket without putting the access token in the URL."""
import hashlib
import secrets
import threading
import time

TICKET_TTL_SECONDS = 30
MAX_OUTSTANDING_PER_USER = 20


class WebSocketTicketStore:
    def __init__(self, ttl_seconds: int = TICKET_TTL_SECONDS, clock=time.monotonic):
        self._ttl = ttl_seconds
        self._clock = clock
        self._lock = threading.Lock()
        self._tickets: dict[str, tuple[int, str, float]] = {}

    @staticmethod
    def _key(ticket: str) -> str:
        return hashlib.sha256(ticket.encode()).hexdigest()

    def _purge(self, now: float) -> None:
        for key in [key for key, (_, _, expires) in self._tickets.items() if expires <= now]:
            del self._tickets[key]

    def issue(self, user_id: int, session_id: str) -> str | None:
        """Return a new ticket bound to the user's session, or None if the user holds too many unused ones."""
        now = self._clock()
        with self._lock:
            self._purge(now)
            if sum(1 for owner, _, _ in self._tickets.values() if owner == user_id) >= MAX_OUTSTANDING_PER_USER:
                return None
            ticket = secrets.token_urlsafe(32)
            self._tickets[self._key(ticket)] = (user_id, session_id, now + self._ttl)
            return ticket

    def consume(self, ticket: str) -> tuple[int, str] | None:
        """Return (user_id, session_id) and invalidate the ticket; None if unknown, used or expired."""
        if not isinstance(ticket, str) or not ticket:
            return None
        now = self._clock()
        with self._lock:
            entry = self._tickets.pop(self._key(ticket), None)
            self._purge(now)
        if entry is None or entry[2] <= now:
            return None
        return entry[0], entry[1]


tickets = WebSocketTicketStore()
