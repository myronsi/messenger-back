from contextlib import asynccontextmanager

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from server.routes import auth, messages, chats, users, groups, media, requests as approval_requests
from server.websocket import router as websocket_router
from fastapi.middleware.trustedhost import TrustedHostMiddleware
from server.database import setup_database
from server.config import allowed_hosts, cors_origins
from server.logging_config import configure_logging
from server.version import __version__

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
app.add_middleware(
    CORSMiddleware,
    allow_origins=cors_origins(),
    allow_credentials=True,
    allow_methods=["GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"],
    allow_headers=["Authorization", "Content-Type", "Accept"],
)

@app.get("/")
def root():
    return {"Server is running"}


@app.get("/version")
def version():
    return {"version": __version__}
