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
        setChats([...chats, { id: data.chat_id, name: targetUser }]);
        setTargetUser('');
      } else {
        alert(data.detail);
      }
    } catch (err) {
      alert('Ошибка сети. Проверьте подключение.');
    }
  };

  return (
    <div id="chats-section">
      <h2>Your Chats</h2>
      <div id="chats-list">
        {chats.map((chat) => (
          <div
            key={chat.id}
            className="chat-item"
            onClick={() => onChatOpen(chat.id, chat.name)}
          >
            {chat.name}
          </div>
        ))}
      </div>
      <input
        id="chat-username"
        type="text"
        placeholder="Username for chat"
        value={targetUser}
        onChange={(e) => setTargetUser(e.target.value)}
      />
      <button id="create-chat-btn" onClick={handleCreateChat}>Create a chat</button>
    </div>
  );
};

export default ChatsListComponent;