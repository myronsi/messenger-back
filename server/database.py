import sqlite3
from pathlib import Path

DB_PATH = "server/messenger.db"

def get_connection():
    conn = sqlite3.connect(DB_PATH, check_same_thread=False)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA journal_mode=WAL")  # Parallel work
    return conn

def setup_database():
    conn = get_connection()
    cursor = conn.cursor()

    # Users table with avatar_url, bio, encrypted_cloud_part, salt, and verification_ciphertext
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS users (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            username TEXT UNIQUE NOT NULL,
            password TEXT NOT NULL,
            avatar_url TEXT,
            bio TEXT,
            encrypted_cloud_part TEXT,
            salt BLOB,
            verification_ciphertext TEXT
        )
    """)

    # Add columns to users if not exist
    for column, definition in [
        ("avatar_url", "TEXT"),
        ("bio", "TEXT"),
        ("encrypted_cloud_part", "TEXT"),
        ("salt", "BLOB"),
        ("verification_ciphertext", "TEXT")
    ]:
        try:
            cursor.execute(f"ALTER TABLE users ADD COLUMN {column} {definition}")
        except sqlite3.OperationalError as e:
            if "duplicate column name" not in str(e).lower():
                raise

    # Check if chats table exists and has the old schema
    cursor.execute("PRAGMA table_info(chats)")
    columns = [col['name'] for col in cursor.fetchall()]
    needs_migration = 'user1_id' in columns and 'user2_id' in columns and 'type' in columns

    if needs_migration:
        # Create a new chats table with nullable user1_id and user2_id
        cursor.execute("""
            CREATE TABLE chats_new (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                name TEXT NOT NULL,
                type TEXT NOT NULL DEFAULT 'one-on-one',
                user1_id INTEGER,
                user2_id INTEGER,
                FOREIGN KEY (user1_id) REFERENCES users (id),
                FOREIGN KEY (user2_id) REFERENCES users (id)
            )
        """)

        # Copy data from old chats table to new one
        cursor.execute("""
            INSERT INTO chats_new (id, name, type, user1_id, user2_id)
            SELECT id, name, COALESCE(type, 'one-on-one'), user1_id, user2_id
            FROM chats
        """)

        # Drop the old chats table
        cursor.execute("DROP TABLE chats")

        # Rename the new table to chats
        cursor.execute("ALTER TABLE chats_new RENAME TO chats")

    else:
        # Create chats table if it doesn't exist
        cursor.execute("""
            CREATE TABLE IF NOT EXISTS chats (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                name TEXT NOT NULL,
                type TEXT NOT NULL DEFAULT 'one-on-one',
                user1_id INTEGER,
                user2_id INTEGER,
                FOREIGN KEY (user1_id) REFERENCES users (id),
                FOREIGN KEY (user2_id) REFERENCES users (id)
            )
        """)

    # Groups table for group chat metadata
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS groups (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            chat_id INTEGER NOT NULL,
            admin_id INTEGER NOT NULL,
            FOREIGN KEY (chat_id) REFERENCES chats (id),
            FOREIGN KEY (admin_id) REFERENCES users (id)
        )
    """)

    # Participants table for chat memberships
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS participants (
            chat_id INTEGER NOT NULL,
            user_id INTEGER NOT NULL,
            PRIMARY KEY (chat_id, user_id),
            FOREIGN KEY (chat_id) REFERENCES chats (id),
            FOREIGN KEY (user_id) REFERENCES users (id)
        )
    """)

    # Messages table with sender_name and reactions
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
            reactions TEXT DEFAULT '[]',
            FOREIGN KEY (chat_id) REFERENCES chats (id),
            FOREIGN KEY (reply_to) REFERENCES messages (id)
        )
    """)

    # Add columns to messages if not exist
    for column, definition in [
        ("edited_at", "DATETIME DEFAULT NULL"),
        ("sender_name", "TEXT NOT NULL"),
        ("reactions", "TEXT DEFAULT '[]'")  # Add reactions column
    ]:
        try:
            cursor.execute(f"ALTER TABLE messages ADD COLUMN {column} {definition}")
        except sqlite3.OperationalError as e:
            if "duplicate column name" not in str(e).lower():
                raise

    # Ensure existing chats have type='one-on-one'
    cursor.execute("""
        UPDATE chats SET type = 'one-on-one' WHERE type IS NULL
    """)

    conn.commit()
    conn.close()

setup_database()