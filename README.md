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
- **Backend**: Python (Flask, SQLite, WebSocket)
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

#### Windows
`\Scripts\activate.bat`  Windows Command Prompt<br>
`\Scripts\Activate.ps1`  Windows PowerShell

### Install dependencies

#### Windows
`pip install fastapi uvicorn websockets`

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

## Usage

### Change directory
`cd messenger`


### Launch python virtual environment
`python -m venv .`

### Activate python virtual environment
#### macOS/Linux
`source bin/activate`

#### Windows
`\Scripts\activate.bat`  Windows Command Prompt<br>
`\Scripts\Activate.ps1`  Windows PowerShell

### Launch server
`uvicorn server.main:app --reload`

### View swagger api
`http://127.0.0.1:8000/docs#/`

### View index.html page
just open the `index.html` file in `/client/` in your browser


## Project Structure
messenger/
├── client/
│   ├── app.js             # Core client-side JavaScript logic
│   ├── index.html         # Main client interface
│   └── style.css          # Styling for the web client
│   
├── server/
│   ├── connection_manager.py  # Handles WebSocket connections
│   ├── database.py            # Database models and queries
│   ├── main.py                # Entry point for the server
│   ├── websocket.py           # WebSocket communication logic
│   ├── messanger.db           # SQLite database file
│   └── routes/
│       ├── auth.py            # User authentication routes
│       ├── chats.py           # Chat-related routes
│       └── messages.py        # Message-related routes
├── LICENSE                 # License file
└── README.md               # Project documentation
 
