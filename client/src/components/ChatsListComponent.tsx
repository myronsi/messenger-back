import React, { useState, useEffect } from 'react';
import { Chat } from '../types';
import { Menu } from 'lucide-react';

interface ChatsListComponentProps {
  username: string;
  onChatOpen: (chatId: number, chatName: string) => void;
  setIsProfileOpen: (open: boolean) => void;
}

const BASE_URL = "http://192.168.178.29:8000";
const DEFAULT_AVATAR = "/static/avatars/default.jpg";

const ChatsListComponent: React.FC<ChatsListComponentProps> = ({ username, onChatOpen, setIsProfileOpen }) => {
  const [chats, setChats] = useState<Chat[]>([]);
  const [targetUser, setTargetUser] = useState('');
  const token = localStorage.getItem('access_token');

  useEffect(() => {
    const fetchChats = async () => {
      try {
        const response = await fetch(`${BASE_URL}/chats/list/${username}`, {
          headers: { Authorization: `Bearer ${token}` },
        });
        if (!response.ok) {
          if (response.status === 401) {
            alert('Сессия истекла. Войдите снова.');
            localStorage.removeItem('access_token');
            window.location.href = '/';
            return;
          }
          throw new Error('Ошибка загрузки чатов');
        }
        const data = await response.json();
        setChats(data.chats);
      } catch (err) {
        console.error('Ошибка при загрузке чатов:', err);
        alert('Не удалось загрузить чаты');
      }
    };
    if (token) fetchChats();
  }, [username, token]);

  const handleCreateChat = async () => {
    if (!targetUser.trim()) {
      alert('Введите имя пользователя');
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
        alert('Чат создан!');
        const newChat = {
          id: data.chat_id,
          name: `Chat: ${username} & ${targetUser}`,
          interlocutor_name: targetUser,
          avatar_url: DEFAULT_AVATAR, // Аватарка будет обновлена при следующем fetchChats
          interlocutor_deleted: false
        };
        setChats([...chats, newChat]);
        setTargetUser('');
      } else {
        alert(data.detail || 'Ошибка при создании чата');
      }
    } catch (err) {
      alert('Ошибка сети. Проверьте подключение.');
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
            className="flex items-center bg-blue-500 text-white p-3 rounded cursor-pointer hover:bg-blue-600 transition-colors"
            onClick={() => onChatOpen(chat.id, chat.interlocutor_name)}
          >
            <img
              src={`${BASE_URL}${chat.avatar_url || DEFAULT_AVATAR}`}
              alt={chat.interlocutor_name}
              className="w-10 h-10 rounded-full mr-3"
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
    </div>
  );
};

export default ChatsListComponent;