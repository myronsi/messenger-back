# Messenger Application

This project is a simple web-based messenger application designed for sending and receiving real-time messages using a client-server architecture. It includes a web client interface and a Python-based server for handling connections, user authentication, and message storage.

---

## Table of Contents
- [Features](#features)
- [Technologies Used](#technologies-used)
- [Installation](#installation)
- [Usage](#usage)
- [Project Structure](#project-structure)

---

## Features
- Real-time messaging using WebSocket.
- User authentication and session management.
- Message storage in PostgreSQL.
- RESTful API for user and chat management.
- Simple and responsive web-based client interface.

---

## Technologies Used
- **Frontend**: React, TypeScript
- **Backend**: Python (FastAPI, PostgreSQL, WebSocket)
- **Database**: PostgreSQL

---

## Installation

### Clone Git-Repository
`git clone https://github.com/myronsi/messenger.git`

### Change directory
`cd messenger`


### Launch python virtual environment
`python -m venv .`

### Activate python virtual environment
#### macOS/Linux
`source bin/activate`

### Install dependencies

#### Arch Linux
`sudo pacman -S python`<br>
`pip install -r requirements.txt`

#### Debian/Ubuntu
`sudo apt update`<br>
`sudo apt install python3 python3-pip`<br>
`pip3 install -r requirements.txt`

#### macOS
`brew install python`<br>
`pip3 install -r requirements.txt`

### Install npm (in client directory only)

`npm i`<br>
or<br>
`npm i --legacy-peer-deps`<br>

## Usage

### Change your app.js file

at first line change `const BASE_URL = "http://ip:8000";` to yours ip addres

### Launch python virtual environment
`python -m venv .`

### Activate python virtual environment
#### macOS/Linux
`source bin/activate`

### Launch server
`uvicorn server.main:app --host 0.0.0.0 --port 8000`

### View swagger api
`http://your_ip:8000/docs#/`

### View messenger
run `npm start` (in client directory)

## Configuration: `SECRET_KEY`

The server signs login, recovery and two-factor tokens with `SECRET_KEY` and
derives the key that encrypts stored TOTP secrets from it. There is no default:
the server refuses to start unless `SECRET_KEY` is set to a random value of at
least 32 characters. Generate one with:

```bash
python -c "import secrets; print(secrets.token_urlsafe(48))"
```

Keep it out of version control (see `.env.example`). Changing the key
invalidates every issued access token, recovery token and 2FA challenge, so all
users have to log in again. Stored TOTP secrets are encrypted with a key derived
from `SECRET_KEY` too, so after a rotation 2FA users can no longer use their
authenticator app (their recovery codes still work) and must re-enrol 2FA.
Earlier versions shipped a publicly known default signing key; deployments that
ran with it must set a new key and should treat existing sessions as compromised.

## Logging

Logging is configured once in `server/logging_config.py` (called from `server/main.py`); modules only use `logging.getLogger(__name__)`. Set `LOG_LEVEL` (default `INFO`) to change verbosity.

Rules for log statements (enforced by `tests/test_log_hygiene.py`):

- Never log recovery shares, master keys, tokens, passwords, TOTP secrets, usernames used for recovery, or message contents / file names.
- Log event types and identifiers only (`chat_id`, `message_id`, `user_id`).
- Do not use `print()` or `logging.basicConfig()` in `server/`.
- Credentials sent in a URL query (e.g. `/ws/chat/0?token=...`) are masked in uvicorn's logs.

### Existing logs may contain secrets

Earlier versions wrote recovery shares, the recovered master key, recovery parts and full message payloads to the logs. After upgrading:

1. Delete or archive-and-encrypt every log produced before this change (container logs: `docker compose logs` history, `docker logs` json files, any log shipping or aggregation system).
2. Treat accounts that registered or recovered while the old logging was active as exposed: the logged shares are enough to recover the account, so ask those users to regenerate their recovery data, and rotate any JWT `SECRET_KEY` that may have been logged.
## Media access

Uploads, voice messages and avatars under `/static` are not public. `GET /static/<path>` needs a signed-in session, sent either as `Authorization: Bearer <access token>` or as the `media_session` cookie (set by login, 2FA login, registration and refresh, scoped to `/static`, httponly). The cookie is what lets plain `<img>`/`<audio>` tags work; `fetch()` calls to `/static` from another origin must use `credentials: 'include'` (or send the bearer header). The session is checked on every request, so logging out or revoking a session cuts access immediately.

- Chat attachments (`uploads/`, `vm/`) are served only to members of a chat that has a message carrying the file, and only while that message is visible to them (not deleted for them, not undelivered to them). The link between file and message is stored in `message_attachments`, written by the upload, voice and forward endpoints. It is never inferred from message text, so a forged message cannot unlock someone else's file.
- User avatars follow the owner's `avatar_visibility` privacy setting; the owner always sees their own.
- Group avatars are served to the group's members and to users with a pending invite.
- `avatars/default.jpg`, `avatars/group.png` and `avatars/deleted.jpg` are public placeholders.
- Missing and forbidden files both answer `404`; requests without a valid session answer `401`. Files that are not images, audio, video or PDF are always sent as downloads with `X-Content-Type-Options: nosniff`.

**Behind a reverse proxy.** Browsers only send a cookie when its `Path` covers the public URL. If nginx serves the API under a prefix (for example `location /api/`), set `COOKIE_PATH_PREFIX=/api` in `.env` so the `media_session` and `refresh_token` cookies are scoped to `/api/static` and `/api/auth`; without it media returns `401` and sessions cannot refresh. Also set `COOKIE_SECURE=true` when the site is served over HTTPS. Both default to the local-development behaviour (no prefix, not `Secure`). After changing them, users need to sign in again once.

Files still live on local disk in `static/`. Object storage with signed URLs (see `docs/image-storage-plan.md`) is a possible later step.
## Docker (Linux)

Install Docker Engine and the Docker Compose plugin, create a `.env` file next to
`compose.yaml` containing `SECRET_KEY=<generated value>`, then start the backend
from the repository root:

```bash
docker compose up --build -d
```

The API is available at `http://localhost:8000`, with interactive API
documentation at `http://localhost:8000/docs`. The `messenger_postgres` and
`messenger_static` Docker volumes retain the PostgreSQL data and uploaded files
across container recreation. Stop the backend with:

```bash
docker compose down
```

For a non-Docker deployment, set `DATABASE_URL` to a PostgreSQL connection URL,
such as `postgresql://messenger:password@localhost:5432/messenger`. The
application creates its schema at startup.

See [the image storage plan](docs/image-storage-plan.md) for a staged approach
to moving image bytes from static files into PostgreSQL.


## Project Structure
<pre>
messenger/
├── README.md
├── LICENSE
├── requirements.txt
├── client/
│   ├── README.md
│   ├── package.json
│   ├── tsconfig.json
│   ├── public/
│   │   ├── index.html
│   │   ├── manifest.json
│   │   └── robots.txt
│   └── src/
│       ├── App.tsx
│       ├── index.tsx
│       ├── styles.css
│       ├── types.ts
│       └── components/
│           ├── ChatComponent.tsx
│           ├── ChatsListComponent.tsx
│           ├── ContextMenuComponent.tsx
│           ├── LoginComponent.tsx
│           ├── RegisterComponent.tsx
│           └── .gitignore
└── server/
    ├── database.py
    ├── logging_config.py
    ├── main.py
    ├── websocket.py
    └── routes/
        ├── auth.py
        ├── chats.py
        └── messages.py
</pre>

## Versioning and releases

This project follows Semantic Versioning. See [docs/versioning.md](docs/versioning.md) for the scheme and [docs/releasing.md](docs/releasing.md) for the release checklist.

