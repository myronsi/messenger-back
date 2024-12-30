from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from server.routes import auth, messages, chats
from server.websocket import router as websocket_router

app = FastAPI()

# Connecting routes
app.include_router(auth.router, prefix="/auth")
app.include_router(messages.router, prefix="/messages")
app.include_router(websocket_router, prefix="")
app.include_router(chats.router, prefix="/chats")
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],  # Allow access from any domains
    allow_credentials=True,
    allow_methods=["*"],  # Allow all methods (POST, GET, OPTIONS, etc.)
    allow_headers=["*"],  # Allow any headers
)

@app.get("/")
def root():
    return {"message": "Добро пожаловать в веб-мессенджер"}
