# Messenger Application

## Description
This is a simple messenger application designed to support user registration, login and personal messaging. The project is structured into `client` and `server` directories, where the frontend and backend are implemented, respectively.

## Features
- **User Authentication**: Includes login and registration.
- **Real-Time Messaging**: Supports real-time personal chats using WebSockets.

### Key Files
- **`client/`**: Contains Javascript files for UI and user interaction.
- **`server/`**: Implements APIs and WebSocket handlers for the backend.
- **`requirements.txt`**: Lists Python dependencies (if applicable).
- **`notes.txt`**: Contains project-related notes and developer comments.

## Installation
**`git clone https://github.com/myronsi/messanger.git`**

### Windows
**`pip install fastapi uvicorn sqlite3 websockets`**

### Arch Linux
**`sudo pacman -S python`**<br>
**`pip install fastapi uvicorn sqlite3 websockets`**

### Debian/Ubuntu
**`sudo apt update`**<br>
**`sudo apt install python3 python3-pip`**<br>
**`pip3 install fastapi uvicorn sqlite3 websockets`**

### macOS
**`brew install python`**<br>
**`pip3 install fastapi uvicorn sqlite3 websockets`**

## Usage

### Change directory
`cd messanger`


### Launch python virtual environment
`python -m venv .`


### Activate python virtual environment
#### macOS/Linux
`source /bin/activate`

#### Windows
`\Scripts\activate.bat`  Windows Command Prompt
`\Scripts\Activate.ps1`  Windows PowerShell


### Launch server
`uvicorn server.main:app --reload`


### View swagger api
`http://127.0.0.1:8000/docs#/`


### View index.html page

just open the `index.html` file in `/client/` in your browser
