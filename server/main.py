from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from server.routes import auth, messages, chats
from server.websocket import router as websocket_router

app = FastAPI()

# Подключение маршрутов
app.include_router(auth.router, prefix="/auth")
app.include_router(messages.router, prefix="/messages")
app.include_router(websocket_router, prefix="")
app.include_router(chats.router, prefix="/chats")
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],  # Разрешить доступ с любых доменов
    allow_credentials=True,
    allow_methods=["*"],  # Разрешить все методы (POST, GET, OPTIONS и т.д.)
    allow_headers=["*"],  # Разрешить любые заголовки
)

@app.get("/")
def root():
    return {"message": "Добро пожаловать в веб-мессенджер"}
