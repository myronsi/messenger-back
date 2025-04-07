import React, { useState, useEffect } from 'react';
import { Chat } from '../types';

interface ChatsListComponentProps {
  username: string;
  onChatOpen: (chatId: number, chatName: string) => void;
}

const ChatsListComponent: React.FC<ChatsListComponentProps> = ({ username, onChatOpen }) => {
  const [chats, setChats] = useState<Chat[]>([]);
  const [targetUser, setTargetUser] = useState('');

  useEffect(() => {
    const fetchChats = async () => {
      try {
        const response = await fetch(`http://192.168.178.29:8000/chats/list/${username}`, {
          headers: { Authorization: `Bearer ${localStorage.getItem('access_token')}` },
        });
        const data = await response.json();
        setChats(data.chats || []);
      } catch (err) {
        console.error('Ошибка при загрузке чатов:', err);
      }
    };
    fetchChats();
  }, [username]);

  const getInterlocutor = (chatName: string, currentUser: string): string => {
    const users = chatName.replace('Chat: ', '').split(' & ');
    return users.find((user) => user !== currentUser) || 'Неизвестный';
  };

  const handleCreateChat = async () => {
    try {
      const response = await fetch('http://192.168.178.29:8000/chats/create', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${localStorage.getItem('access_token')}`,
        },
        body: JSON.stringify({ user1: username, user2: targetUser }),
      });
      const data = await response.json();
      if (response.ok) {
        alert('Чат создан!');
        setChats([...chats, { id: data.chat_id, name: `Chat: ${username} & ${targetUser}` }]);
        setTargetUser('');
      } else {
        alert(data.detail);
      }
    } catch (err) {
      alert('Ошибка сети. Проверьте подключение.');
    }
  };

  return (
    <div className="space-y-4">
      <h2 className="text-xl font-bold">Ваши чаты</h2>
      <div className="space-y-2">
        {chats.map((chat) => (
          <div
            key={chat.id}
            className="bg-blue-500 text-white p-3 rounded cursor-pointer hover:bg-blue-600 transition-colors"
            onClick={() => onChatOpen(chat.id, getInterlocutor(chat.name, username))}
          >
            {getInterlocutor(chat.name, username)}
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