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
- Message storage in a lightweight SQLite database.
- RESTful API for user and chat management.
- Simple and responsive web-based client interface.

---

## Technologies Used
- **Frontend**: React, TypeScript
- **Backend**: Python (FastAPI, SQLite, WebSocket)
- **Database**: SQLite

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


## Project Structure
└── messenger/
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
        ├── connection_manager.py
        ├── database.py
        ├── main.py
        ├── websocket.py
        └── routes/
            ├── auth.py
            ├── chats.py
            └── messages.py
