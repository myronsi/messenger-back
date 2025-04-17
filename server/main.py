from fastapi import FastAPI, Request, HTTPException
from fastapi.middleware.cors import CORSMiddleware
from server.routes import auth, messages, chats, users, groups
from server.websocket import router as websocket_router
from starlette.responses import JSONResponse
from fastapi.middleware.trustedhost import TrustedHostMiddleware
from fastapi.staticfiles import StaticFiles

app = FastAPI()

# ALLOWED_IPS = {"192.168.178.29"}

# @app.middleware("http")
# async def restrict_by_ip(request: Request, call_next):
#     client_ip = request.client.host

#     if client_ip is None:
#         return JSONResponse(status_code=400, content={"detail": "Cannot determine client IP"})

#     if client_ip not in ALLOWED_IPS:
#         return JSONResponse(status_code=403, content={"detail": "Access forbidden"})

#     return await call_next(request)

app.mount("/static", StaticFiles(directory="static"), name="static")

app.include_router(auth.router, prefix="/auth", tags=["auth"])
app.include_router(messages.router, prefix="/messages", tags=["messages"])
app.include_router(websocket_router, prefix="")
app.include_router(chats.router, prefix="/chats", tags=["chats"])
app.include_router(users.router, prefix="/users", tags=["users"])
app.include_router(groups.router, prefix="/groups", tags=["groups"])
app.add_middleware(TrustedHostMiddleware, allowed_hosts=["*"])
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

@app.get("/")
def root():
    return {"Server is running"}
