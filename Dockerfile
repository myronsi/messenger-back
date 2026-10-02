FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /app

RUN apt-get update \
    && apt-get install --no-install-recommends -y ffmpeg ssss \
    && rm -rf /var/lib/apt/lists/*

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY server ./server
COPY static ./static

# Keep an immutable copy so the built-in avatars are restored when /app/static is a volume.
COPY static /opt/default-static

RUN mkdir -p /app/static

EXPOSE 8000

CMD ["sh", "-c", "mkdir -p /app/static/avatars && cp -f /opt/default-static/avatars/default.jpg /opt/default-static/avatars/deleted.jpg /opt/default-static/avatars/group.png /app/static/avatars/ && exec uvicorn server.main:app --host 0.0.0.0 --port 8000"]
