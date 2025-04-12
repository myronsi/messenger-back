import sqlite3
from pathlib import Path

DB_PATH = "server/messenger.db"

def get_connection():
    conn = sqlite3.connect(DB_PATH, check_same_thread=False)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA journal_mode=WAL")  # Paral. work
    return conn

def setup_database():
    conn = get_connection()
    cursor = conn.cursor()

    # Users table with avatar_url and bio (no deleted field)
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS users (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            username TEXT UNIQUE NOT NULL,
            password TEXT NOT NULL,
            avatar_url TEXT,
            bio TEXT
        )
    """)

    # Add columns to users if not exist
    for column, definition in [
        ("avatar_url", "TEXT"),
        ("bio", "TEXT")
    ]:
        try:
            cursor.execute(f"ALTER TABLE users ADD COLUMN {column} {definition}")
        except sqlite3.OperationalError as e:
            if "duplicate column name" not in str(e).lower():
                raise

    # Message table with sender_name
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS messages (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            chat_id INTEGER NOT NULL,
            sender_id INTEGER NOT NULL,
            sender_name TEXT NOT NULL,
            content TEXT NOT NULL,
            timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
            edited_at DATETIME DEFAULT NULL,
            reply_to INTEGER DEFAULT NULL,
            FOREIGN KEY (chat_id) REFERENCES chats (id),
            FOREIGN KEY (reply_to) REFERENCES messages (id)
        )
    """)

    # Add columns to messages if not exist
    for column, definition in [
        ("edited_at", "DATETIME DEFAULT NULL"),
        ("sender_name", "TEXT NOT NULL")
    ]:
        try:
            cursor.execute(f"ALTER TABLE messages ADD COLUMN {column} {definition}")
        except sqlite3.OperationalError as e:
            if "duplicate column name" not in str(e).lower():
                raise

    # Chat table
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS chats (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT NOT NULL,
            user1_id INTEGER NOT NULL,
            user2_id INTEGER NOT NULL,
            FOREIGN KEY (user1_id) REFERENCES users (id),
            FOREIGN KEY (user2_id) REFERENCES users (id)
        )
    """)

    # Update messages table for default chat
    cursor.execute("""
        UPDATE messages
        SET chat_id = 1
        WHERE sender_id IN (
            SELECT id FROM users WHERE username IN ('user1', 'user2')
        )
    """)

    # Chat participants table
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS participants (
            chat_id INTEGER NOT NULL,
            user_id INTEGER NOT NULL,
            PRIMARY KEY (chat_id, user_id),
            FOREIGN KEY (chat_id) REFERENCES chats (id),
            FOREIGN KEY (user_id) REFERENCES users (id)
        )
    """)

    conn.commit()
    conn.close()

setup_database()
