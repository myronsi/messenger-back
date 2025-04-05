import sqlite3
from pathlib import Path

# Path to the database
DB_PATH = "server/messenger.db"

# Function to connect to the database
def get_connection():
    conn = sqlite3.connect(DB_PATH, check_same_thread=False)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA journal_mode=WAL")  # Allow parallel work
    return conn

# Creating tables
def setup_database():
    conn = get_connection()
    cursor = conn.cursor()

    # Users table
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS users (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            username TEXT UNIQUE NOT NULL,
            password TEXT NOT NULL
        )
    """)

    # Message table
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS messages (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            chat_id INTEGER NOT NULL,
            sender_id INTEGER NOT NULL,
            content TEXT NOT NULL,
            timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
            edited_at DATETIME DEFAULT NULL,
            FOREIGN KEY (chat_id) REFERENCES chats (id),
            FOREIGN KEY (sender_id) REFERENCES users (id)
        )
    """)


    # Add 'edited_at' column if it doesn't exist
    try:
        cursor.execute("ALTER TABLE messages ADD COLUMN edited_at DATETIME DEFAULT NULL")
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

    # Update messages table for default chat (if necessary)
    cursor.execute("""
        UPDATE messages
        SET chat_id = 1
        WHERE sender_id IN (
            SELECT id FROM users WHERE username IN ('user1', 'user2')
        )
    """)

    # Таблица участников чата (если нужно хранить участников отдельно)
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

# Initialize the database
setup_database()
