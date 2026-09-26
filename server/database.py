import sqlite3
import os
from pathlib import Path
from server.time_utils import utc_now_iso

DB_PATH = os.getenv("MESSENGER_DB_PATH", "server/messenger.db")
Path(DB_PATH).parent.mkdir(parents=True, exist_ok=True)

def get_connection():
    conn = sqlite3.connect(DB_PATH, timeout=30, check_same_thread=False)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA journal_mode=WAL")  # Parallel work
    conn.execute("PRAGMA busy_timeout=30000")
    return conn

def setup_database():
    conn = get_connection()
    cursor = conn.cursor()

    # Users table with display_name, avatar_url, bio, encrypted_cloud_part, salt, and verification_ciphertext
    cursor.execute("""
        CREATE TABLE IF NOT EXISTS users (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            username TEXT UNIQUE NOT NULL,
            display_name TEXT NOT NULL,
            password TEXT NOT NULL,
            avatar_url TEXT,
            bio TEXT,
            created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            last_seen DATETIME DEFAULT CURRENT_TIMESTAMP,
            encrypted_cloud_part TEXT,
            salt BLOB,
            verification_ciphertext TEXT
        )
    """)

    # Add columns to users if not exist
    for column, definition in [
        ("display_name", "TEXT NOT NULL DEFAULT ''"),
        ("avatar_url", "TEXT"),
        ("bio", "TEXT"),
        ("created_at", "DATETIME"),
        ("last_seen", "DATETIME"),
        ("encrypted_cloud_part", "TEXT"),
        ("salt", "BLOB"),
        ("verification_ciphertext", "TEXT")
    ]:
        try:
            cursor.execute(f"ALTER TABLE users ADD COLUMN {column} {definition}")
        except sqlite3.OperationalError as e:
            if "duplicate column name" not in str(e).lower():
                raise

    cursor.execute("""
        UPDATE users
        SET display_name = username
        WHERE display_name IS NULL OR TRIM(display_name) = ''
    """)

    cursor.execute("""
        UPDATE users
        SET created_at = COALESCE(last_seen, ?)
        WHERE created_at IS NULL
    """, (utc_now_iso(),))

    cursor.execute("""
        UPDATE users
        SET last_seen = ?
        WHERE last_seen IS NULL
    """, (utc_now_iso(),))

    cursor.execute("""
        CREATE TABLE IF NOT EXISTS user_avatar_history (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            user_id INTEGER NOT NULL,
            avatar_url TEXT NOT NULL,
            created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            is_current INTEGER NOT NULL DEFAULT 0,
            FOREIGN KEY (user_id) REFERENCES users (id)
        )
    """)

    cursor.execute("""
        INSERT INTO user_avatar_history (user_id, avatar_url, created_at, is_current)
        SELECT users.id, users.avatar_url, COALESCE(users.last_seen, ?), 1
        FROM users
        WHERE users.avatar_url IS NOT NULL
          AND TRIM(users.avatar_url) != ''
          AND NOT EXISTS (
              SELECT 1
              FROM user_avatar_history
              WHERE user_avatar_history.user_id = users.id
          )
    """, (utc_now_iso(),))

    cursor.execute("""
        CREATE TABLE IF NOT EXISTS user_privacy_settings (
            user_id INTEGER PRIMARY KEY,
            avatar_visibility TEXT NOT NULL DEFAULT 'everyone',
            profile_visibility TEXT NOT NULL DEFAULT 'everyone',
            presence_visibility TEXT NOT NULL DEFAULT 'everyone',
            read_receipts_enabled INTEGER NOT NULL DEFAULT 1,
            direct_messages TEXT NOT NULL DEFAULT 'everyone',
            group_invites TEXT NOT NULL DEFAULT 'everyone',
            search_visibility TEXT NOT NULL DEFAULT 'everyone',
            FOREIGN KEY (user_id) REFERENCES users (id)
        )
    """)

    cursor.execute("""
        INSERT OR IGNORE INTO user_privacy_settings (user_id)
        SELECT id FROM users
    """)

    cursor.execute("""
        CREATE TABLE IF NOT EXISTS user_privacy_exceptions (
            owner_id INTEGER NOT NULL,
            setting_key TEXT NOT NULL,
            target_user_id INTEGER NOT NULL,
            effect TEXT NOT NULL,
            created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            PRIMARY KEY (owner_id, setting_key, target_user_id),
            FOREIGN KEY (owner_id) REFERENCES users (id),
            FOREIGN KEY (target_user_id) REFERENCES users (id)
        )
    """)

    cursor.execute("""
        CREATE TABLE IF NOT EXISTS approval_requests (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            type TEXT NOT NULL,
            requester_id INTEGER NOT NULL,
            recipient_id INTEGER NOT NULL,
            status TEXT NOT NULL DEFAULT 'pending',
            message_text TEXT,
            chat_id INTEGER,
            created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            responded_at DATETIME,
            FOREIGN KEY (requester_id) REFERENCES users (id),
            FOREIGN KEY (recipient_id) REFERENCES users (id),
            FOREIGN KEY (chat_id) REFERENCES chats (id)
        )
    """)

    cursor.execute("""
        CREATE TABLE IF NOT EXISTS user_security_settings (
            user_id INTEGER PRIMARY KEY,
            session_duration_days INTEGER NOT NULL DEFAULT 90,
            two_factor_enabled INTEGER NOT NULL DEFAULT 0,
            two_factor_secret TEXT,
            pending_two_factor_secret TEXT,
            updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            FOREIGN KEY (user_id) REFERENCES users (id)
        )
    """)

    cursor.execute("""
        INSERT OR IGNORE INTO user_security_settings (user_id)
        SELECT id FROM users
    """)

    cursor.execute("""
        CREATE TABLE IF NOT EXISTS user_sessions (
            id TEXT PRIMARY KEY,
            user_id INTEGER NOT NULL,
            refresh_token_hash TEXT NOT NULL,
            user_agent TEXT,
            ip_address TEXT,
            created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            last_active_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            expires_at DATETIME NOT NULL,
            revoked_at DATETIME,
            FOREIGN KEY (user_id) REFERENCES users (id)
        )
    """)

    cursor.execute("""
        CREATE TABLE IF NOT EXISTS user_2fa_recovery_codes (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            user_id INTEGER NOT NULL,
            code_hash TEXT NOT NULL,
            created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            used_at DATETIME,
            FOREIGN KEY (user_id) REFERENCES users (id)
        )
    """)

    cursor.execute("""
        CREATE TABLE IF NOT EXISTS user_blocks (
            blocker_id INTEGER NOT NULL,
            blocked_id INTEGER NOT NULL,
            created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            PRIMARY KEY (blocker_id, blocked_id),
            FOREIGN KEY (blocker_id) REFERENCES users (id),
            FOREIGN KEY (blocked_id) REFERENCES users (id)
        )
    """)

    cursor.execute("""
        CREATE TABLE IF NOT EXISTS user_contact_names (
            owner_id INTEGER NOT NULL,
            target_id INTEGER NOT NULL,
            display_name TEXT NOT NULL,
            updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            PRIMARY KEY (owner_id, target_id),
            FOREIGN KEY (owner_id) REFERENCES users (id),
            FOREIGN KEY (target_id) REFERENCES users (id)
        )
    """)

    # Check if chats table exists and has the old schema
    cursor.execute("PRAGMA table_info(chats)")
    chat_columns_info = cursor.fetchall()
    columns = [col['name'] for col in chat_columns_info]
    columns_by_name = {col['name']: col for col in chat_columns_info}
    needs_migration = (
        'user1_id' in columns_by_name
        and 'user2_id' in columns_by_name
        and (
            columns_by_name['user1_id']['notnull']
            or columns_by_name['user2_id']['notnull']
        )
    )

    if needs_migration:
        # Create a new chats table with nullable user1_id and user2_id
        cursor.execute("""
            CREATE TABLE chats_new (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                name TEXT NOT NULL,
                type TEXT NOT NULL DEFAULT 'one-on-one',
                description TEXT DEFAULT '',
                avatar_url TEXT,
                user1_id INTEGER,
                user2_id INTEGER,
                FOREIGN KEY (user1_id) REFERENCES users (id),
                FOREIGN KEY (user2_id) REFERENCES users (id)
            )
        """)

        # Copy data from old chats table to new one
        description_select = "description" if "description" in columns else "''"
        avatar_select = "avatar_url" if "avatar_url" in columns else "NULL"
        cursor.execute("""
            INSERT INTO chats_new (id, name, type, description, avatar_url, user1_id, user2_id)
            SELECT id, name, COALESCE(type, 'one-on-one'), COALESCE({description_select}, ''), {avatar_select}, user1_id, user2_id
            FROM chats
        """.format(description_select=description_select, avatar_select=avatar_select))

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
                description TEXT DEFAULT '',
                avatar_url TEXT,
                user1_id INTEGER,
                user2_id INTEGER,
                FOREIGN KEY (user1_id) REFERENCES users (id),
                FOREIGN KEY (user2_id) REFERENCES users (id)
            )
        """)

    # Add group/customization metadata columns to chats if not exist.
    for column, definition in [
        ("description", "TEXT DEFAULT ''"),
        ("avatar_url", "TEXT")
    ]:
        try:
            cursor.execute(f"ALTER TABLE chats ADD COLUMN {column} {definition}")
        except sqlite3.OperationalError as e:
            if "duplicate column name" not in str(e).lower():
                raise

    group_avatar_dir = Path("static/avatars/groups")
    if group_avatar_dir.exists():
        cursor.execute("""
            SELECT id
            FROM chats
            WHERE type = 'group'
              AND (
                  avatar_url IS NULL
                  OR TRIM(avatar_url) = ''
                  OR avatar_url = '/static/avatars/group.png'
              )
        """)
        for group in cursor.fetchall():
            avatar_files = list(group_avatar_dir.glob(f"group_{group['id']}_*"))
            if not avatar_files:
                continue
            latest_avatar = max(avatar_files, key=lambda path: path.stat().st_mtime)
            cursor.execute(
                "UPDATE chats SET avatar_url = ? WHERE id = ? AND type = 'group'",
                (f"/static/avatars/groups/{latest_avatar.name}", group["id"]),
            )

    cursor.execute("""
        UPDATE chats
        SET avatar_url = '/static/avatars/group.png'
        WHERE type = 'group'
          AND (avatar_url IS NULL OR TRIM(avatar_url) = '')
    """)

    cursor.execute("""
        CREATE TABLE IF NOT EXISTS user_chat_pins (
            user_id INTEGER NOT NULL,
            chat_id INTEGER NOT NULL,
            pinned_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            PRIMARY KEY (user_id, chat_id),
            FOREIGN KEY (user_id) REFERENCES users (id),
            FOREIGN KEY (chat_id) REFERENCES chats (id)
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
            role TEXT NOT NULL DEFAULT 'member',
            PRIMARY KEY (chat_id, user_id),
            FOREIGN KEY (chat_id) REFERENCES chats (id),
            FOREIGN KEY (user_id) REFERENCES users (id)
        )
    """)

    try:
        cursor.execute("ALTER TABLE participants ADD COLUMN role TEXT NOT NULL DEFAULT 'member'")
    except sqlite3.OperationalError as e:
        if "duplicate column name" not in str(e).lower():
            raise

    cursor.execute("""
        UPDATE participants
        SET role = 'member'
        WHERE role IS NULL OR TRIM(role) = ''
    """)

    cursor.execute("""
        UPDATE participants
        SET role = 'owner'
        WHERE EXISTS (
            SELECT 1
            FROM groups
            WHERE groups.chat_id = participants.chat_id
              AND groups.admin_id = participants.user_id
        )
    """)

    # Messages table with sender_name, reactions, and read_by
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
            read_by TEXT DEFAULT '[]',
            delivery_error TEXT DEFAULT NULL,
            undelivered_to TEXT DEFAULT '[]',
            audio_duration REAL DEFAULT NULL,
            audio_waveform TEXT DEFAULT NULL,
            forwarded_from_message_id INTEGER DEFAULT NULL,
            forwarded_from_sender_id INTEGER DEFAULT NULL,
            forwarded_from_sender_name TEXT DEFAULT NULL,
            forwarded_from_sender_username TEXT DEFAULT NULL,
            FOREIGN KEY (chat_id) REFERENCES chats (id),
            FOREIGN KEY (reply_to) REFERENCES messages (id)
        )
    """)

    # Add columns to messages if not exist
    for column, definition in [
        ("edited_at", "DATETIME DEFAULT NULL"),
        ("sender_name", "TEXT NOT NULL"),
        ("reactions", "TEXT DEFAULT '[]'"),
        ("read_by", "TEXT DEFAULT '[]'"),
        ("delivery_error", "TEXT DEFAULT NULL"),
        ("undelivered_to", "TEXT DEFAULT '[]'"),
        ("audio_duration", "REAL DEFAULT NULL"),
        ("audio_waveform", "TEXT DEFAULT NULL"),
        ("forwarded_from_message_id", "INTEGER DEFAULT NULL"),
        ("forwarded_from_sender_id", "INTEGER DEFAULT NULL"),
        ("forwarded_from_sender_name", "TEXT DEFAULT NULL"),
        ("forwarded_from_sender_username", "TEXT DEFAULT NULL"),
        ("deleted_for", "TEXT DEFAULT '[]'")
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
