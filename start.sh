#!/bin/bash

python -m venv .

source bin/activate

uvicorn server.main:app --host 0.0.0.0 --port 8000
