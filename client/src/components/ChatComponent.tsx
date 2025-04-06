import React, { useState, useEffect, useRef } from 'react';
import { Message } from '../types';
import ContextMenuComponent from './ContextMenuComponent';

interface ChatComponentProps {
  chatId: number;
  chatName: string;
  username: string;
  onBack: () => void;
}

const WS_URL = "ws://192.168.178.29:8000";
const BASE_URL = "http://192.168.178.29:8000";

const ChatComponent: React.FC<ChatComponentProps> = ({ chatId, chatName, username, onBack }) => {
  const [messages, setMessages] = useState<Message[]>([]);
  const [messageInput, setMessageInput] = useState('');
  const [contextMenu, setContextMenu] = useState<{ x: number; y: number; messageId: number } | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const token = localStorage.getItem('access_token');

  // Загрузка сообщений и инициализация WebSocket
  useEffect(() => {
    const loadMessages = async () => {
      try {
        const response = await fetch(`${BASE_URL}/messages/history/${chatId}`, {
          headers: { Authorization: `Bearer ${token}` },
        });
        if (response.ok) {
          const data = await response.json();
          setMessages(data.history || []);
        } else if (response.status === 401) {
          alert('Сессия истекла. Войдите снова.');
          localStorage.removeItem('access_token');
          onBack();
        } else {
          console.error('Ошибка загрузки сообщений:', await response.json());
        }
      } catch (err) {
        console.error('Ошибка сети при загрузке сообщений:', err);
      }
    };

    // Проверка токена перед любыми действиями
    if (!token) {
      console.error('Токен отсутствует. WebSocket и сообщения не будут загружены.');
      setMessages([]);
      return;
    }

    loadMessages();

    // Создание WebSocket только если он ещё не существует или закрыт
    if (!wsRef.current || wsRef.current.readyState === WebSocket.CLOSED) {
      wsRef.current = new WebSocket(`${WS_URL}/ws/chat/${chatId}?token=${token}`);

      wsRef.current.onopen = () => {
        console.log('WebSocket подключён к чату', chatId);
      };

      wsRef.current.onmessage = (event) => {
        const parsedData = JSON.parse(event.data);
        const { username: sender, data, timestamp } = parsedData;
        setMessages((prev) => [
          ...prev,
          { id: data.message_id, sender, content: data.content, timestamp },
        ]);
      };

      wsRef.current.onerror = (error) => {
        console.error('WebSocket ошибка:', error);
      };

      wsRef.current.onclose = (event) => {
        console.log('WebSocket закрыт. Код:', event.code, 'Причина:', event.reason);
      };
    }

    // Очистка WebSocket при размонтировании компонента
    return () => {
      if (wsRef.current && wsRef.current.readyState === WebSocket.OPEN) {
        wsRef.current.close();
      }
    };
  }, [chatId, token, onBack]);

  // Отправка сообщения
  const handleSendMessage = () => {
    if (!messageInput.trim() || !wsRef.current || wsRef.current.readyState !== WebSocket.OPEN) {
      console.error('WebSocket не готов или сообщение пустое');
      return;
    }
    const messageData = { chat_id: chatId, content: messageInput };
    wsRef.current.send(JSON.stringify(messageData));
    setMessageInput('');
  };

  // Удаление чата
  const handleDeleteChat = async () => {
    // eslint-disable-next-line no-restricted-globals
    if (!confirm('Вы уверены, что хотите удалить этот чат?')) return;
    try {
      const response = await fetch(`${BASE_URL}/chats/delete/${chatId}`, {
        method: 'DELETE',
        headers: { Authorization: `Bearer ${token}` },
      });
      if (response.ok) {
        alert('Чат удалён!');
        onBack();
      } else {
        const error = await response.json();
        alert(`Ошибка: ${error.detail}`);
      }
    } catch (err) {
      alert('Ошибка сети.');
    }
  };

  // Обработка контекстного меню
  const handleContextMenu = (e: React.MouseEvent, messageId: number) => {
    e.preventDefault();
    const menuWidth = 150;
    const menuHeight = 100;
    let x = e.clientX;
    let y = e.clientY;
    if (x + menuWidth > window.innerWidth) x = window.innerWidth - menuWidth;
    if (y + menuHeight > window.innerHeight) y = window.innerHeight - menuHeight;
    setContextMenu({ x, y, messageId });
  };

  // Редактирование сообщения
  const handleEditMessage = async (messageId: number) => {
    const message = messages.find((m) => m.id === messageId);
    const newContent = prompt('Введите новое сообщение:', message?.content);
    if (!newContent) return;
    try {
      const response = await fetch(`${BASE_URL}/messages/edit/${messageId}`, {
        method: 'PUT',
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${token}`,
        },
        body: JSON.stringify({ content: newContent }),
      });
      if (response.ok) {
        setMessages((prev) =>
          prev.map((m) => (m.id === messageId ? { ...m, content: newContent } : m))
        );
        alert('Сообщение обновлено!');
      } else {
        const error = await response.json();
        alert(`Ошибка: ${error.detail}`);
      }
    } catch (err) {
      alert('Ошибка сети.');
    }
    setContextMenu(null);
  };

  // Удаление сообщения
  const handleDeleteMessage = async (messageId: number) => {
    // eslint-disable-next-line no-restricted-globals
    if (!confirm('Вы уверены, что хотите удалить это сообщение?')) return;
    try {
      const response = await fetch(`${BASE_URL}/messages/delete/${messageId}`, {
        method: 'DELETE',
        headers: { Authorization: `Bearer ${token}` },
      });
      if (response.ok) {
        setMessages((prev) => prev.filter((m) => m.id !== messageId));
        alert('Сообщение удалено!');
      } else {
        const error = await response.json();
        alert(`Ошибка: ${error.detail}`);
      }
    } catch (err) {
      alert('Ошибка сети.');
    }
    setContextMenu(null);
  };

  return (
    <div id="chat-section" style={{ width: '100%', height: '100%', display: 'flex', flexDirection: 'column' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', paddingBottom: '10px' }}>
        <h2 id="chat-name">{chatName}</h2>
        <div>
          <button id="back-to-chats-btn" onClick={onBack}>
            Back to chats
          </button>
          <button id="delete-chat-btn" onClick={handleDeleteChat}>
            Delete Chat
          </button>
        </div>
      </div>
      <div id="chat-window">
        {messages.map((msg) => (
          <div
            key={msg.id}
            className="message"
            data-message-id={msg.id}
            onContextMenu={(e) => handleContextMenu(e, msg.id)}
          >
            <span>
              {msg.timestamp} - {msg.sender}: {msg.content}
            </span>
          </div>
        ))}
      </div>
      <div style={{ display: 'flex', paddingTop: '10px' }}>
        <input
          id="message-input"
          type="text"
          placeholder="Enter your message"
          value={messageInput}
          onChange={(e) => setMessageInput(e.target.value)}
        />
        <button id="send-btn" onClick={handleSendMessage}>
          Send
        </button>
      </div>
      {contextMenu && (
        <ContextMenuComponent
          x={contextMenu.x}
          y={contextMenu.y}
          onEdit={() => handleEditMessage(contextMenu.messageId)}
          onDelete={() => handleDeleteMessage(contextMenu.messageId)}
        />
      )}
    </div>
  );
};

export default ChatComponent;