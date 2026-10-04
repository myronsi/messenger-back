"""Client/server compatibility: version endpoint, X-Client-Api-Version checks and the per-version request counter."""
import json
import logging
import re
from collections import Counter
from threading import Lock

from server.config import min_client_api_version
from server.version import API_VERSION, __version__, build_commit

logger = logging.getLogger(__name__)

CLIENT_VERSION_HEADER = "x-client-version"
CLIENT_API_VERSION_HEADER = "x-client-api-version"

_VERSION_RE = re.compile(
    r"^(\d{1,6})\.(\d{1,6})\.(\d{1,6})(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$"
)
MAX_VERSION_CHARS = 64
MAX_TRACKED_VERSIONS = 50
MAX_TRACKED_CLIENTS = 200
# Endpoints a client must always be able to reach to find out why it is rejected.
EXEMPT_PATHS = {"/", "/version", "/metrics"}


def parse_version(value: str | None) -> tuple | None:
    """Parse a SemVer string into a key that sorts by SemVer precedence: (major, minor, patch, prerelease).

    A pre-release sorts below its release (1.0.0-alpha.3 < 1.0.0); build metadata is ignored.
    """
    value = (value or "").strip()
    match = _VERSION_RE.match(value) if len(value) <= MAX_VERSION_CHARS else None
    if not match:
        return None
    major, minor, patch, prerelease = match.groups()
    if prerelease is None:
        pre_key = (1,)
    else:
        identifiers = tuple((0, int(part)) if part.isdigit() else (1, part) for part in prerelease.split("."))
        pre_key = (0, *identifiers)
    return int(major), int(minor), int(patch), pre_key


def validate_configuration(environ=None) -> None:
    """Fail at startup when MIN_CLIENT_API_VERSION could not be applied, instead of erroring on requests."""
    raw = min_client_api_version(environ)
    minimum = parse_version(raw)
    server = parse_version(API_VERSION)
    if minimum is None:
        raise ValueError(f"MIN_CLIENT_API_VERSION must look like 1.0.0, got {raw!r}")
    if minimum[0] != server[0] or minimum > server:
        raise ValueError(
            f"MIN_CLIENT_API_VERSION {raw} must have major version {server[0]} and not be above API version {API_VERSION}"
        )


def version_info() -> dict:
    return {"version": __version__, "commit": build_commit()}


def hello_event(environ=None) -> dict:
    """First WebSocket event: lets clients that reconnect after a deploy notice the new contract."""
    return {
        "type": "hello",
        "api_version": API_VERSION,
        "min_client_api_version": min_client_api_version(environ),
    }


def check_client_api_version(value: str, environ=None) -> str | None:
    """Return None when the client may continue, otherwise a human readable reason."""
    client = parse_version(value)
    if client is None:
        return "invalid"
    server = parse_version(API_VERSION)
    minimum = parse_version(min_client_api_version(environ))
    if minimum is None:
        raise ValueError("MIN_CLIENT_API_VERSION is not a valid version")
    if client[0] != server[0]:
        return f"API major version {client[0]} is not supported, this server speaks {server[0]}.x"
    if client < minimum:
        return f"API version {value} is older than the minimum supported {min_client_api_version(environ)}"
    return None


class ClientVersionCounter:
    """Requests per client API version. Values come from the client, so the number of series is capped."""

    def __init__(self, limit: int = MAX_TRACKED_VERSIONS, client_limit: int = MAX_TRACKED_CLIENTS):
        self._limit = limit
        self._client_limit = client_limit
        self._counts: Counter = Counter()
        self._clients: set = set()
        self._lock = Lock()

    @staticmethod
    def _label(value: str | None) -> str:
        return "none" if not value else value if parse_version(value) else "invalid"

    def record(self, api_version: str | None, app_version: str | None = None) -> str:
        """Count a request and log every new (app version, API version) pair once, whether accepted or not."""
        label = self._label(api_version)
        app_label = self._label(app_version)
        with self._lock:
            if label not in self._counts and len(self._counts) >= self._limit:
                label = "other"
            self._counts[label] += 1
            pair = (app_label, label)
            first_seen = pair not in self._clients and len(self._clients) < self._client_limit
            if first_seen:
                self._clients.add(pair)
        if first_seen:
            logger.info("New client seen: client_version=%s client_api_version=%s", app_label, label)
        return label

    def snapshot(self) -> dict[str, int]:
        with self._lock:
            return dict(self._counts)

    def render_prometheus(self) -> str:
        lines = [
            "# HELP messenger_client_api_requests_total HTTP requests per X-Client-Api-Version.",
            "# TYPE messenger_client_api_requests_total counter",
        ]
        for label, count in sorted(self.snapshot().items()):
            lines.append(f'messenger_client_api_requests_total{{client_api_version="{label}"}} {count}')
        return "\n".join(lines) + "\n"


client_versions = ClientVersionCounter()


def _problem(status: int, code: str, title: str, detail: str, environ=None) -> tuple[bytes, list]:
    body = {
        "type": "about:blank",
        "title": title,
        "status": status,
        "detail": detail,
        "code": code,
        "api_version": API_VERSION,
        "min_client_api_version": min_client_api_version(environ),
    }
    payload = json.dumps(body).encode()
    headers = [(b"content-type", b"application/problem+json"), (b"content-length", str(len(payload)).encode())]
    return payload, headers


class ClientVersionMiddleware:
    """Counts and logs client versions and answers 426 to clients built for an unsupported API contract.

    Requests without X-Client-Api-Version (old clients, curl, health checks) are let through.
    """

    def __init__(self, app):
        self.app = app

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http" or scope["method"] == "OPTIONS":
            await self.app(scope, receive, send)
            return

        headers = {key.decode("latin-1").lower(): value.decode("latin-1") for key, value in scope["headers"]}
        api_version = headers.get(CLIENT_API_VERSION_HEADER)
        app_version = headers.get(CLIENT_VERSION_HEADER)
        label = client_versions.record(api_version, app_version)
        if scope["path"] in EXEMPT_PATHS or api_version is None:
            await self.app(scope, receive, send)
            return

        reason = check_client_api_version(api_version)
        if reason is None:
            await self.app(scope, receive, send)
            return

        logger.warning(
            "Rejected %s %s: client_api_version=%s client_version=%s",
            scope["method"], scope["path"], label, ClientVersionCounter._label(app_version),
        )
        if reason == "invalid":
            status, code, title = 400, "invalid_client_version", "Invalid client version"
            reason = "X-Client-Api-Version must look like 2.4.0"
        else:
            status, code, title = 426, "client_outdated", "Upgrade Required"
        payload, response_headers = _problem(status, code, title, reason)
        await send({"type": "http.response.start", "status": status, "headers": response_headers})
        await send({"type": "http.response.body", "body": payload})
