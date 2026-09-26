FROM python:3.12-slim

WORKDIR /app

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

COPY requirements.txt /app/requirements.txt
RUN pip install --no-cache-dir -r /app/requirements.txt \
    && python -c "import uvicorn"

COPY server /app/server

RUN mkdir -p /app/static /app/data \
    && adduser --disabled-password --gecos "" --uid 1000 appuser \
    && chown -R appuser:appuser /app

EXPOSE 8000

USER appuser

CMD ["uvicorn", "server.main:app", "--host", "0.0.0.0", "--port", "8000"]
