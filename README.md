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
- **Frontend**: HTML, CSS, JavaScript
- **Backend**: Python (FastAPI, SQLite, WebSocket)
- **Database**: SQLite

---

## Installation

### Clone Git-Repository
`git clone https://github.com/myronsi/messenger.git`

### Change directory
`cd messenger2.0`


### Launch python virtual environment
`python -m venv .`

### Activate python virtual environment
#### macOS/Linux
`source bin/activate`

### Install dependencies

#### Arch Linux
`sudo pacman -S python`<br>
`pip install fastapi uvicorn websockets`

#### Debian/Ubuntu
`sudo apt update`<br>
`sudo apt install python3 python3-pip`<br>
`pip3 install fastapi uvicorn websockets`

#### macOS
`brew install python`<br>
`pip3 install fastapi uvicorn websockets`

### Install npm (in client directory only)

`npm i`<br>
or<br>
`npm i --legacy-peer-deps`<br>

## Usage

### Change directory
`cd messenger2.0`

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
run `npm run dev -- --host` (in client directory)

copy "Network: http://your_link/" and paste in your browser


## Project Structure
messenger/<br>
├── client/<br>
│⠀⠀⠀├── app.js             # Core client-side JavaScript logic<br>
│⠀⠀⠀├── index.html         # Main client interface<br>
│⠀⠀⠀└── style.css          # Styling for the web client<br>
│   <br>
├── server/<br>
│⠀⠀⠀├── connection_manager.py  # Handles WebSocket connections<br>
│⠀⠀⠀├── database.py            # Database models and queries<br>
│⠀⠀⠀├── main.py                # Entry point for the server<br>
│⠀⠀⠀├── websocket.py           # WebSocket communication logic<br>
│⠀⠀⠀├── messanger.db           # SQLite database file<br>
│⠀⠀⠀└── routes/<br>
│   ⠀⠀⠀⠀⠀⠀├── auth.py            # User authentication routes<br>
│   ⠀⠀⠀⠀⠀⠀├── chats.py           # Chat-related routes<br>
│   ⠀⠀⠀⠀⠀⠀└── messages.py        # Message-related routes<br>
├── LICENSE                 # License file<br>
└── README.md               # Project documentation<br>
