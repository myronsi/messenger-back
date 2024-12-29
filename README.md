# Messenger Application

## Description
This is a simple messenger application designed to support user registration, login and personal messaging. The project is structured into `client` and `server` directories, where the frontend and backend are implemented, respectively.

## Features
- **User Authentication**: Includes login, registration, and password reset via email.
- **Real-Time Messaging**: Supports real-time personal chats using WebSockets.

### Key Files
- **`client/`**: Contains Javascript files for UI and user interaction.
- **`server/`**: Implements APIs and WebSocket handlers for the backend.
- **`requirements.txt`**: Lists Python dependencies (if applicable).
- **`notes.txt`**: Contains project-related notes and developer comments.

## Installation
git clone https://github.com/myronsi/messanger.git

### Windows
**`pip install fastapi uvicorn sqlite3 websockets`**

### Arch Linux
**`sudo pacman -S python
pip install fastapi uvicorn sqlite3 websockets`**

### Debian/Ubuntu
**`sudo apt update
sudo apt install python3 python3-pip
pip3 install fastapi uvicorn sqlite3 websockets`**

### macOS
**`brew install python
pip3 install fastapi uvicorn sqlite3 websockets`**
