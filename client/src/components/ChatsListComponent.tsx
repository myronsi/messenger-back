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

const BASE_URL = "http://192.168.178.29:8000";
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
        const newChat = {
          id: data.chat_id,
          name: `Chat: ${username} & ${targetUser}`,
          interlocutor_name: targetUser,
          avatar_url: DEFAULT_AVATAR,
          interlocutor_deleted: false,
        };
        setChats([...chats, newChat]);
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
            modal.type === 'success' || modal.type === 'error' || modal.type === 'validation' || modal.type === 'deletedUser'
              ? 'OK'
              : 'Подтвердить'
          }
          isError={modal.type === 'success' || modal.type === 'error' || modal.type === 'validation' || modal.type === 'deletedUser'}
        />
      )}
    </div>
  );
};

export default ChatsListComponent;