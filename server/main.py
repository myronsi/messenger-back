from contextlib import asynccontextmanager

from fastapi import FastAPI, HTTPException
from fastapi.responses import PlainTextResponse
from fastapi.middleware.cors import CORSMiddleware
from server.routes import auth, messages, chats, users, groups, media, requests as approval_requests
from server.websocket import router as websocket_router
from fastapi.middleware.trustedhost import TrustedHostMiddleware
from server.database import setup_database
from server.config import allowed_hosts, cors_origins, metrics_enabled
from server.client_version import ClientVersionMiddleware, client_versions, version_info
from server.logging_config import configure_logging

configure_logging()


@asynccontextmanager
async def lifespan(_app: FastAPI):
    setup_database()
    yield


app = FastAPI(lifespan=lifespan)

# /static is served by an authenticated route; files are never exposed without an access check.
app.include_router(media.router)

app.include_router(auth.router, prefix="/auth", tags=["auth"])
app.include_router(messages.router, prefix="/messages", tags=["messages"])
app.include_router(websocket_router, prefix="")
app.include_router(chats.router, prefix="/chats", tags=["chats"])
app.include_router(users.router, prefix="/users", tags=["users"])
app.include_router(groups.router, prefix="/groups", tags=["groups"])
app.include_router(approval_requests.router, prefix="/requests", tags=["requests"])
trusted_hosts = allowed_hosts()
if trusted_hosts:
    app.add_middleware(TrustedHostMiddleware, allowed_hosts=trusted_hosts)
app.add_middleware(ClientVersionMiddleware)
# Added last so it is outermost and also adds CORS headers to 426 responses.
app.add_middleware(
    CORSMiddleware,
    allow_origins=cors_origins(),
    allow_credentials=True,
    allow_methods=["GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"],
    allow_headers=["Authorization", "Content-Type", "Accept", "X-Client-Version", "X-Client-Api-Version"],
)

@app.get("/")
def root():
    return {"Server is running"}


@app.get("/version", tags=["meta"])
def version():
    return version_info()


@app.get("/metrics", response_class=PlainTextResponse, include_in_schema=False)
def metrics():
    if not metrics_enabled():
        raise HTTPException(status_code=404)
    return client_versions.render_prometheus()
