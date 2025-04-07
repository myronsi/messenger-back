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

const getTime = (timestamp: string): string => {
  const date = new Date(timestamp);
  return date.toTimeString().substring(0, 8);
};

const getDateString = (date: Date): string => {
  return date.toISOString().substring(0, 10);
};

const formatDateLabel = (timestamp: string): string => {
  const messageDate = new Date(timestamp);
  const today = new Date();
  const yesterday = new Date(today);
  yesterday.setDate(today.getDate() - 1);

  const diffInDays = Math.floor((today.getTime() - messageDate.getTime()) / (1000 * 60 * 60 * 24));
  const isSameYear = messageDate.getFullYear() === today.getFullYear();

  if (getDateString(messageDate) === getDateString(today)) {
    return 'Сегодня';
  } else if (getDateString(messageDate) === getDateString(yesterday)) {
    return 'Вчера';
  } else if (diffInDays <= 7) {
    const daysOfWeek = ['Воскресенье', 'Понедельник', 'Вторник', 'Среда', 'Четверг', 'Пятница', 'Суббота'];
    return daysOfWeek[messageDate.getDay()];
  } else if (isSameYear) {
    const months = [
      'января', 'февраля', 'марта', 'апреля', 'мая', 'июня',
      'июля', 'августа', 'сентября', 'октября', 'ноября', 'декабря',
    ];
    return `${messageDate.getDate()} ${months[messageDate.getMonth()]}`;
  } else {
    const months = [
      'января', 'февраля', 'марта', 'апреля', 'мая', 'июня',
      'июля', 'августа', 'сентября', 'октября', 'ноября', 'декабря',
    ];
    return `${messageDate.getDate()} ${months[messageDate.getMonth()]} ${messageDate.getFullYear()}`;
  }
};

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

      wsRef.current.onopen = () => console.log('WebSocket подключён к чату', chatId);
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

  const renderMessagesWithSeparators = () => {
    const result: React.ReactNode[] = [];
    let lastDateLabel: string | null = null;

    messages.forEach((msg) => {
      const currentDateLabel = formatDateLabel(msg.timestamp);

      if (currentDateLabel !== lastDateLabel) {
        result.push(
          <div
            key={`separator-${msg.id}`}
            className="text-center text-gray-500 py-2 border-b border-gray-300"
          >
            {currentDateLabel}
          </div>
        );
        lastDateLabel = currentDateLabel;
      }

      const isMine = msg.sender === username;
      result.push(
        <div
          key={msg.id}
          className={`p-3 rounded-lg max-w-[70%] mb-2 ${
            isMine ? 'ml-auto bg-blue-500 text-white' : 'mr-auto bg-gray-200 text-black'
          }`}
          onContextMenu={(e) => handleContextMenu(e, msg.id)}
        >
          <span>
            {getTime(msg.timestamp)} - {msg.sender}: {msg.content}
          </span>
        </div>
      );
    });

    return result;
  };

  return (
    <div className="flex flex-col h-full w-full">
      <div className="flex justify-between items-center pb-4">
        <h2 className="text-2xl font-bold">{chatName}</h2>
        <div className="space-x-2">
          <button
            className="bg-red-500 text-white p-2 rounded hover:bg-red-600 transition-colors"
            onClick={onBack}
          >
            Назад
          </button>
          <button
            className="bg-red-500 text-white p-2 rounded hover:bg-red-600 transition-colors"
            onClick={handleDeleteChat}
          >
            Удалить чат
          </button>
        </div>
      </div>
      <div className="flex-1 overflow-y-auto border border-gray-300 p-4 bg-gray-50 rounded">
        {renderMessagesWithSeparators()}
      </div>
      <div className="flex pt-4">
        <input
          type="text"
          placeholder="Введите сообщение"
          value={messageInput}
          onChange={(e) => setMessageInput(e.target.value)}
          className="flex-1 p-2 border border-gray-300 rounded-l focus:outline-none focus:ring-2 focus:ring-blue-500"
        />
        <button
          className="bg-blue-500 text-white p-2 rounded-r hover:bg-blue-600 transition-colors"
          onClick={handleSendMessage}
        >
          Отправить
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