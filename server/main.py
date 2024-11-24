from fastapi import FastAPI
from server.routes import auth, messages
from server.websocket import router as websocket_router

app = FastAPI()

# Подключение маршрутов
app.include_router(auth.router, prefix="/auth")
app.include_router(messages.router, prefix="/messages")
app.include_router(websocket_router, prefix="")

@app.get("/")
def root():
    return {"message": "Добро пожаловать в веб-мессенджер"}
