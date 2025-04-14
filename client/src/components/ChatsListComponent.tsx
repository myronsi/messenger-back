import React, { useState, useEffect, useRef } from 'react';
import { Chat } from '../types';
import { Menu } from 'lucide-react';
import UserProfileComponent from './UserProfileComponent';
import ConfirmModal from './ConfirmModal';

interface ChatsListComponentProps {
  username: string;
  onChatOpen: (chatId: number, chatName: string, interlocutorDeleted: boolean) => void;
  setIsProfileOpen: (open: boolean) => void;
  activeChatId?: number;
}

// Интерфейс для сообщений WebSocket
interface WebSocketMessage {
  type: 'chat_created' | 'chat_deleted' | 'error';
  message?: string;
  chat?: {
    chat_id: number;
    name: string;
    user1: string;
    user2: string;
    user1_avatar_url?: string;
    user2_avatar_url?: string;
  };
  chat_id?: number;
}

const BASE_URL = "http://192.168.178.29:8000";
const WS_URL = "ws://192.168.178.29:8000";
const DEFAULT_AVATAR = "/static/avatars/default.jpg";

const ChatsListComponent: React.FC<ChatsListComponentProps> = ({
  username,
  onChatOpen,
  setIsProfileOpen,
  activeChatId,
}) => {
  const [chats, setChats] = useState<Chat[]>([]);
  const [targetUser, setTargetUser] = useState('');
  const [selectedUser, setSelectedUser] = useState<string | null>(null);
  const [modal, setModal] = useState<{
    type: 'error' | 'success' | 'validation' | 'deletedUser';
    message: string;
    onConfirm?: () => void;
  } | null>(null);
  const token = localStorage.getItem('access_token');
  const hasFetchedChats = useRef(false);
  const wsRef = useRef<WebSocket | null>(null);

  useEffect(() => {
    const fetchChats = async () => {
      if (hasFetchedChats.current) return;
      hasFetchedChats.current = true;
      try {
        const response = await fetch(`${BASE_URL}/chats/list/${username}`, {
          headers: { Authorization: `Bearer ${token}` },
        });
        if (!response.ok) {
          if (response.status === 401) {
            setModal({
              type: 'error',
              message: 'Сессия истекла. Войдите снова.',
              onConfirm: () => {
                localStorage.removeItem('access_token');
                window.location.href = '/';
              },
            });
            return;
          }
          throw new Error('Ошибка загрузки чатов');
        }
        const data = await response.json();
        setChats(data.chats);
      } catch (err) {
        console.error('Ошибка при загрузке чатов:', err);
        setModal({
          type: 'error',
          message: 'Не удалось загрузить чаты. Попробуйте снова.',
        });
      }
    };

    if (token) fetchChats();

    // Периодический опрос каждые 30 секунд
    const interval = setInterval(() => {
      if (token) {
        hasFetchedChats.current = false;
        fetchChats();
      }
    }, 30000);

    // Подключение WebSocket для уведомлений о чатах
    const connectWebSocket = () => {
      if (wsRef.current && wsRef.current.readyState === WebSocket.OPEN) {
        console.log('WebSocket уже подключён для списка чатов');
        return;
      }

      console.log('Подключение WebSocket для списка чатов');
      wsRef.current = new WebSocket(`${WS_URL}/ws/chat/0?token=${token}`);

      wsRef.current.onopen = () => {
        console.log('WebSocket успешно подключён для списка чатов');
      };

      wsRef.current.onmessage = (event) => {
        let parsedData: WebSocketMessage;
        try {
          parsedData = JSON.parse(event.data);
        } catch (error) {
          console.error('Received non-JSON message:', event.data);
          return;
        }

        const { type, message, chat } = parsedData;

        if (type === 'chat_created' && chat) {
          console.log('Received chat_created:', chat);
          setChats((prev) => {
            if (prev.some((c) => c.id === chat.chat_id)) return prev;
            const interlocutor_name = chat.user1 === username ? chat.user2 : chat.user1;
            // Проверяем, что пользователь является участником чата
            if (![chat.user1, chat.user2].includes(username)) {
              console.log('Ignoring chat_created: user not a participant');
              return prev;
            }
            // Выбираем аватарку собеседника
            const avatar_url =
              chat.user1 === username
                ? chat.user2_avatar_url || DEFAULT_AVATAR
                : chat.user1_avatar_url || DEFAULT_AVATAR;
            return [
              ...prev,
              {
                id: chat.chat_id,
                name: chat.name,
                interlocutor_name,
                avatar_url,
                interlocutor_deleted: false,
              },
            ];
          });
        } else if (type === 'chat_deleted' && parsedData.chat_id !== undefined) {
          console.log('Received chat_deleted:', parsedData.chat_id);
          setChats((prev) => prev.filter((c) => c.id !== parsedData.chat_id));
        } else if (type === 'error' && message) {
          console.error('Server error:', message);
          setModal({
            type: 'error',
            message,
          });
        } else {
          console.warn('Unknown message type or missing data:', parsedData);
        }
      };

      wsRef.current.onerror = (error) => {
        console.error('WebSocket ошибка:', error);
      };

      wsRef.current.onclose = (event) => {
        console.log('WebSocket закрыт. Код:', event.code, 'Причина:', event.reason);
        if (event.code !== 1000 && event.code !== 1005) {
          console.log('Переподключение через 1 секунду...');
          setTimeout(connectWebSocket, 1000);
        }
      };
    };

    if (token) connectWebSocket();

    return () => {
      clearInterval(interval);
      if (wsRef.current && wsRef.current.readyState !== WebSocket.CLOSED) {
        console.log('Очистка: закрываем WebSocket для списка чатов');
        wsRef.current.close();
      }
    };
  }, [username, token]);

  const handleCreateChat = async () => {
    if (!targetUser.trim()) {
      setModal({
        type: 'validation',
        message: 'Введите имя пользователя.',
      });
      return;
    }
    try {
      const response = await fetch(`${BASE_URL}/chats/create`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${token}`,
        },
        body: JSON.stringify({ user1: username, user2: targetUser }),
      });
      const data = await response.json();
      if (response.ok) {
        setModal({
          type: 'success',
          message: 'Чат успешно создан!',
        });
        setTargetUser('');
        setTimeout(() => setModal(null), 1000);
      } else {
        setModal({
          type: 'error',
          message: data.detail || 'Ошибка при создании чата.',
        });
      }
    } catch (err) {
      setModal({
        type: 'error',
        message: 'Ошибка сети. Проверьте подключение.',
      });
    }
  };

  const handleChatClick = (chatId: number, chatName: string, interlocutorDeleted: boolean) => {
    if (chatId === activeChatId) {
      console.log('Чат уже активен, повторное подключение не требуется');
      return;
    }
    onChatOpen(chatId, chatName, interlocutorDeleted);
  };

  const handleUserClick = (user: string, interlocutorDeleted: boolean) => {
    if (interlocutorDeleted) {
      setModal({
        type: 'deletedUser',
        message: 'Информация о удалённом аккаунте недоступна.',
      });
      setTimeout(() => setModal(null), 1500);
    } else {
      setSelectedUser(user);
    }
  };

  return (
    <div className="space-y-4">
      <div className="flex justify-between items-center">
        <h2 className="text-xl font-bold">Ваши чаты</h2>
        <button
          onClick={() => setIsProfileOpen(true)}
          className="text-blue-500 hover:text-blue-600 transition-colors"
        >
          <Menu size={24} />
        </button>
      </div>
      <div className="space-y-2">
        {chats.map((chat) => (
          <div
            key={chat.id}
            className={`flex items-center p-3 rounded cursor-pointer transition-colors ${
              chat.id === activeChatId
                ? 'bg-blue-700 text-white'
                : 'bg-blue-500 text-white hover:bg-blue-600'
            }`}
            onClick={() => handleChatClick(chat.id, chat.interlocutor_name, chat.interlocutor_deleted)}
          >
            <img
              src={`${BASE_URL}${chat.avatar_url || DEFAULT_AVATAR}`}
              alt={chat.interlocutor_name}
              className={`w-10 h-10 rounded-full mr-3 ${
                chat.interlocutor_deleted ? 'cursor-not-allowed opacity-50' : 'cursor-pointer'
              }`}
              onClick={(e) => {
                e.stopPropagation();
                handleUserClick(chat.interlocutor_name, chat.interlocutor_deleted);
              }}
            />
            <span>{chat.interlocutor_name}</span>
          </div>
        ))}
      </div>
      <input
        type="text"
        placeholder="Имя пользователя для чата"
        value={targetUser}
        onChange={(e) => setTargetUser(e.target.value)}
        className="w-full p-2 border border-gray-300 rounded focus:outline-none focus:ring-2 focus:ring-blue-500"
      />
      <button
        onClick={handleCreateChat}
        className="w-full bg-green-500 text-white p-2 rounded hover:bg-green-600 transition-colors"
      >
        Создать чат
      </button>
      {selectedUser && (
        <UserProfileComponent username={selectedUser} onClose={() => setSelectedUser(null)} />
      )}
      {modal && (
        <ConfirmModal
          title={
            modal.type === 'success'
              ? 'Успех'
              : modal.type === 'validation'
              ? 'Ошибка ввода'
              : modal.type === 'deletedUser'
              ? 'Ошибка'
              : 'Ошибка'
          }
          message={modal.message}
          onConfirm={modal.onConfirm || (() => setModal(null))}
          onCancel={() => setModal(null)}
          confirmText={
            modal.type === 'success' ||
            modal.type === 'error' ||
            modal.type === 'validation' ||
            modal.type === 'deletedUser'
              ? 'OK'
              : 'Подтвердить'
          }
          isError={
            modal.type === 'success' ||
            modal.type === 'error' ||
            modal.type === 'validation' ||
            modal.type === 'deletedUser'
          }
        />
      )}
    </div>
  );
};

export default ChatsListComponent;