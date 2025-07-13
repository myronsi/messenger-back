import psycopg2
from psycopg2 import pool

# Database configuration for PostgreSQL
db_config = {
    "dbname": "messenger",
    "user": "postgres",
    "password": "98052",
    "host": "localhost",
    "port": "5432"
}

# Connection pool for efficient management
connection_pool = psycopg2.pool.SimpleConnectionPool(
    1, 20, **db_config  # Minimum 1, maximum 20 connections
)

def get_connection():
    try:
        connection = connection_pool.getconn()
        return connection
    except Exception as e:
        print(f"Database connection error: {e}")
        return None

def release_connection(connection):
    connection_pool.putconn(connection)

def setup_database():
    conn = get_connection()
    if conn is None:
        return
    cursor = conn.cursor()

    # Users table
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS users (
            id SERIAL PRIMARY KEY,
            username VARCHAR(50) UNIQUE NOT NULL,
            password TEXT NOT NULL,
            avatar_url TEXT,
            bio TEXT,
            encrypted_cloud_part TEXT,
            salt BYTEA,
            verification_ciphertext TEXT
        )
    """)

    # Chats table
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS chats (
            id SERIAL PRIMARY KEY,
            name VARCHAR(100) NOT NULL,
            type VARCHAR(20) NOT NULL DEFAULT 'one-on-one',
            user1_id INTEGER,
            user2_id INTEGER,
            FOREIGN KEY (user1_id) REFERENCES users(id),
            FOREIGN KEY (user2_id) REFERENCES users(id)
        )
    """)

    # Groups table
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS groups (
            id SERIAL PRIMARY KEY,
            chat_id INTEGER NOT NULL,
            admin_id INTEGER NOT NULL,
            FOREIGN KEY (chat_id) REFERENCES chats(id),
            FOREIGN KEY (admin_id) REFERENCES users(id)
        )
    """)

    # Participants table
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS participants (
            chat_id INTEGER NOT NULL,
            user_id INTEGER NOT NULL,
            PRIMARY KEY (chat_id, user_id),
            FOREIGN KEY (chat_id) REFERENCES chats(id),
            FOREIGN KEY (user_id) REFERENCES users(id)
        )
    """)

    # Messages table
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS messages (
            id SERIAL PRIMARY KEY,
            chat_id INTEGER NOT NULL,
            sender_id INTEGER NOT NULL,
            sender_name VARCHAR(50) NOT NULL,
            content TEXT NOT NULL,
            timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
            edited_at TIMESTAMP DEFAULT NULL,
            reply_to INTEGER DEFAULT NULL,
            reactions JSONB DEFAULT '[]',
            read_by JSONB DEFAULT '[]',
            FOREIGN KEY (chat_id) REFERENCES chats(id),
            FOREIGN KEY (reply_to) REFERENCES messages(id)
        )
    """)

    conn.commit()
    release_connection(conn)

setup_database()