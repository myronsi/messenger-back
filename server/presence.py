from collections import Counter

from server.time_utils import utc_now_iso


_connections: Counter[int] = Counter()


def mark_user_connected(user_id: int) -> bool:
    was_offline = _connections[user_id] == 0
    _connections[user_id] += 1
    return was_offline


def mark_user_disconnected(user_id: int) -> bool:
    if _connections[user_id] <= 1:
        _connections.pop(user_id, None)
        return True
    _connections[user_id] -= 1
    return False


def is_user_online(user_id: int) -> bool:
    return _connections[user_id] > 0
