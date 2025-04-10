import React, { useState, useEffect } from 'react';
import { Chat } from '../types';

interface ChatsListComponentProps {
  username: string;
  onChatOpen: (chatId: number, chatName: string) => void;
}

const BASE_URL = "http://192.168.178.29:8000";
const DEFAULT_AVATAR = "/static/avatars/default.jpg";

const ChatsListComponent: React.FC<ChatsListComponentProps> = ({ username, onChatOpen }) => {
  const [chats, setChats] = useState<Chat[]>([]);
  const [targetUser, setTargetUser] = useState('');
  const [isModalOpen, setIsModalOpen] = useState(false);
  const [avatarFile, setAvatarFile] = useState<File | null>(null);
  const token = localStorage.getItem('access_token');

  useEffect(() => {
    const fetchChats = async () => {
      try {
        const response = await fetch(`${BASE_URL}/chats/list/${username}`, {
          headers: { Authorization: `Bearer ${token}` },
        });
        if (!response.ok) throw new Error('Ошибка загрузки чатов');
        const data = await response.json();
        const chatsWithAvatars = await Promise.all(
          data.chats.map(async (chat: Chat) => {
            const interlocutor = getInterlocutor(chat.name, username);
            const userResponse = await fetch(`${BASE_URL}/auth/users/${interlocutor}`, {
              headers: { Authorization: `Bearer ${token}` },
            });
            if (userResponse.ok) {
              const userData = await userResponse.json();
              return { ...chat, avatar_url: userData.avatar_url || DEFAULT_AVATAR };
            }
            return { ...chat, avatar_url: DEFAULT_AVATAR };
          })
        );
        setChats(chatsWithAvatars);
      } catch (err) {
        console.error('Ошибка при загрузке чатов:', err);
      }
    };
    if (token) fetchChats();
  }, [username, token]);

  const getInterlocutor = (chatName: string, currentUser: string): string => {
    const users = chatName.replace('Chat: ', '').split(' & ');
    return users.find((user) => user !== currentUser) || 'Неизвестный';
  };

  const handleCreateChat = async () => {
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
        const userResponse = await fetch(`${BASE_URL}/auth/users/${targetUser}`, {
          headers: { Authorization: `Bearer ${token}` },
        });
        let avatarUrl = DEFAULT_AVATAR;
        if (userResponse.ok) {
          const userData = await userResponse.json();
          avatarUrl = userData.avatar_url || DEFAULT_AVATAR;
        }
        const newChat = { id: data.chat_id, name: `Chat: ${username} & ${targetUser}`, avatar_url: avatarUrl };
        setChats([...chats, newChat]);
        setTargetUser('');
      } else {
        alert(data.detail);
      }
    } catch (err) {
      alert('Ошибка сети. Проверьте подключение.');
    }
  };

  const handleAvatarUpload = async () => {
    if (!avatarFile || !token) return;

    const formData = new FormData();
    formData.append('file', avatarFile);

    try {
      const response = await fetch(`${BASE_URL}/auth/me/avatar`, {
        method: 'POST',
        headers: {
          Authorization: `Bearer ${token}`,
        },
        body: formData,
      });
      const data = await response.json();
      if (response.ok) {
        alert('Аватарка успешно обновлена!');
        setChats((prevChats) =>
          prevChats.map((chat) => {
            const interlocutor = getInterlocutor(chat.name, username);
            return interlocutor === username ? { ...chat, avatar_url: data.avatar_url } : chat;
          })
        );
        setIsModalOpen(false);
        setAvatarFile(null);
      } else {
        alert(data.detail || 'Ошибка при загрузке аватарки');
      }
    } catch (err) {
      alert('Ошибка сети при загрузке аватарки');
    }
  };

  return (
    <div className="space-y-4">
      <div className="flex justify-between items-center">
        <h2 className="text-xl font-bold">Ваши чаты</h2>
        <button
          onClick={() => setIsModalOpen(true)}
          className="bg-blue-500 text-white p-2 rounded hover:bg-blue-600 transition-colors"
        >
          Изменить аватарку
        </button>
      </div>
      <div className="space-y-2">
        {chats.map((chat) => (
          <div
            key={chat.id}
            className="flex items-center bg-blue-500 text-white p-3 rounded cursor-pointer hover:bg-blue-600 transition-colors"
            onClick={() => onChatOpen(chat.id, getInterlocutor(chat.name, username))}
          >
            <img
              src={`${BASE_URL}${chat.avatar_url || DEFAULT_AVATAR}`}
              alt={getInterlocutor(chat.name, username)}
              className="w-10 h-10 rounded-full mr-3"
            />
            <span>{getInterlocutor(chat.name, username)}</span>
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

      {isModalOpen && (
        <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center">
          <div className="bg-white p-6 rounded shadow-lg w-96">
            <h3 className="text-lg font-bold mb-4">Загрузить аватарку</h3>
            <input
              type="file"
              accept="image/*"
              onChange={(e) => setAvatarFile(e.target.files?.[0] || null)}
              className="mb-4 w-full"
            />
            <div className="flex justify-end space-x-2">
              <button
                onClick={() => setIsModalOpen(false)}
                className="bg-gray-300 text-black p-2 rounded hover:bg-gray-400 transition-colors"
              >
                Отмена
              </button>
              <button
                onClick={handleAvatarUpload}
                disabled={!avatarFile}
                className={`p-2 rounded text-white transition-colors ${
                  avatarFile ? 'bg-blue-500 hover:bg-blue-600' : 'bg-gray-500 cursor-not-allowed'
                }`}
              >
                Загрузить
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};

export default ChatsListComponent;