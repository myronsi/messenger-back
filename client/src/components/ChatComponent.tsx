import React, { useState, useEffect, useRef } from 'react';
import { Message } from '../types';
import ContextMenuComponent from './ContextMenuComponent';
import UserProfileComponent from './UserProfileComponent';
import ConfirmModal from './ConfirmModal';

interface ChatComponentProps {
  chatId: number;
  chatName: string;
  username: string;
  interlocutorDeleted: boolean;
  onBack: () => void;
}

const WS_URL = "ws://192.168.178.29:8000";
const BASE_URL = "http://192.168.178.29:8000";
const DEFAULT_AVATAR = "/static/avatars/default.jpg";

const getTime = (timestamp: string): string => {
  const date = new Date(timestamp);
  return date.toTimeString().substring(0, 5);
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

  if (getDateString(messageDate) === getDateString(today)) return 'Сегодня';
  if (getDateString(messageDate) === getDateString(yesterday)) return 'Вчера';
  if (diffInDays <= 7) {
    const daysOfWeek = ['Воскресенье', 'Понедельник', 'Вторник', 'Среда', 'Четверг', 'Пятница', 'Суббота'];
    return daysOfWeek[messageDate.getDay()];
  }
  if (isSameYear) {
    const months = ['января', 'февраля', 'марта', 'апреля', 'мая', 'июня', 'июля', 'августа', 'сентября', 'октября', 'ноября', 'декабря'];
    return `${messageDate.getDate()} ${months[messageDate.getMonth()]}`;
  }
  const months = ['января', 'февраля', 'марта', 'апреля', 'мая', 'июня', 'июля', 'августа', 'сентября', 'октября', 'ноября', 'декабря'];
  return `${messageDate.getDate()} ${months[messageDate.getMonth()]} ${messageDate.getFullYear()}`;
};

const shortenText = (text: string, maxLength: number = 50): string => {
  if (text.length <= maxLength) return text;
  return text.substring(0, maxLength) + '...';
};

const ChatComponent: React.FC<ChatComponentProps> = ({ chatId, chatName, username, interlocutorDeleted, onBack }) => {
  const [messages, setMessages] = useState<Message[]>([]);
  const [messageInput, setMessageInput] = useState('');
  const [contextMenu, setContextMenu] = useState<{ x: number; y: number; messageId: number; isMine: boolean } | null>(null);
  const [replyTo, setReplyTo] = useState<Message | null>(null);
  const [editingMessage, setEditingMessage] = useState<Message | null>(null);
  const [selectedUser, setSelectedUser] = useState<string | null>(null);
  const [modal, setModal] = useState<{
    type: 'deleteMessage' | 'deleteChat' | 'error' | 'copy' | 'deletedUser';
    message: string;
    onConfirm?: () => void;
  } | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const chatWindowRef = useRef<HTMLDivElement>(null);
  const contextMenuRef = useRef<HTMLDivElement>(null);
  const token = localStorage.getItem('access_token');
  const hasFetchedMessages = useRef(false);

  const scrollToBottom = () => {
    if (chatWindowRef.current) {
      chatWindowRef.current.scrollTo({
        top: chatWindowRef.current.scrollHeight,
        behavior: 'smooth',
      });
    }
  };

  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (contextMenuRef.current && !contextMenuRef.current.contains(event.target as Node) && contextMenu) {
        setContextMenu(null);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, [contextMenu]);

  useEffect(() => {
    const loadMessages = async () => {
      if (hasFetchedMessages.current) return;
      hasFetchedMessages.current = true;
      try {
        const response = await fetch(`${BASE_URL}/messages/history/${chatId}`, {
          headers: { Authorization: `Bearer ${token}` },
        });
        if (response.ok) {
          const data = await response.json();
          const loadedMessages = data.history.map((msg: Message) => ({
            ...msg,
            avatar_url: msg.avatar_url || DEFAULT_AVATAR,
            reply_to: msg.reply_to || null,
          })) || [];
          setMessages(loadedMessages);
        } else if (response.status === 401) {
          setModal({
            type: 'error',
            message: 'Сессия истекла. Войдите снова.',
          });
          localStorage.removeItem('access_token');
          setTimeout(onBack, 2000);
        } else {
          const error = await response.json();
          setModal({
            type: 'error',
            message: `Ошибка загрузки сообщений: ${error.detail || 'Неизвестная ошибка'}`,
          });
        }
      } catch (err) {
        setModal({
          type: 'error',
          message: 'Ошибка сети при загрузке сообщений.',
        });
      }
    };

    if (!token) {
      setModal({
        type: 'error',
        message: 'Токен отсутствует. WebSocket и сообщения не будут загружены.',
      });
      setMessages([]);
      return;
    }

    loadMessages();

    if (interlocutorDeleted) {
      console.log('Собеседник удалён, WebSocket не подключается для чата', chatId);
      return;
    }

    const connectWebSocket = () => {
      if (wsRef.current && wsRef.current.readyState === WebSocket.OPEN) {
        console.log('WebSocket уже подключён для чата', chatId);
        return;
      }

      console.log('Подключение WebSocket для чата', chatId);
      wsRef.current = new WebSocket(`${WS_URL}/ws/chat/${chatId}?token=${token}`);

      wsRef.current.onopen = () => {
        console.log('WebSocket успешно подключён к чату', chatId);
      };

      wsRef.current.onmessage = (event) => {
        const parsedData = JSON.parse(event.data);
        const { type } = parsedData;

        if (type === "message") {
          const { username: sender, data, timestamp, avatar_url, is_deleted } = parsedData;
          if (data.chat_id !== chatId) {
            console.log(`Игнорируем сообщение для другого chatId: ${data.chat_id}`);
            return;
          }
          const newMessage = {
            id: data.message_id,
            sender,
            content: data.content,
            timestamp,
            avatar_url: avatar_url || DEFAULT_AVATAR,
            reply_to: data.reply_to || null,
            is_deleted: is_deleted || false,
          };
          setMessages((prev) => {
            if (prev.some((msg) => msg.id === newMessage.id)) {
              return prev;
            }
            return [...prev, newMessage];
          });
        } else if (type === "edit") {
          const { message_id, new_content } = parsedData;
          setMessages((prev) =>
            prev.map((msg) => (msg.id === message_id ? { ...msg, content: new_content } : msg))
          );
          setEditingMessage(null);
          setMessageInput('');
        } else if (type === "delete") {
          const { message_id } = parsedData;
          setMessages((prev) => prev.filter((msg) => msg.id !== message_id));
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

    connectWebSocket();

    return () => {
      if (wsRef.current && wsRef.current.readyState !== WebSocket.CLOSED) {
        console.log('Очистка: закрываем WebSocket для чата', chatId);
        wsRef.current.close();
      }
      hasFetchedMessages.current = false;
    };
  }, [chatId, token, onBack, interlocutorDeleted]);

  useEffect(() => {
    scrollToBottom();
  }, [messages]);

  const handleSendMessage = () => {
    if (!messageInput.trim() || !wsRef.current || wsRef.current.readyState !== WebSocket.OPEN) return;

    if (editingMessage) {
      const editData = {
        type: "edit",
        message_id: editingMessage.id,
        content: messageInput,
      };
      wsRef.current.send(JSON.stringify(editData));
    } else {
      const messageData = {
        type: "message",
        content: messageInput,
        reply_to: replyTo ? replyTo.id : null,
      };
      wsRef.current.send(JSON.stringify(messageData));
    }

    setMessageInput('');
    setReplyTo(null);
    setEditingMessage(null);
    setTimeout(scrollToBottom, 0);
  };

  const handleDeleteChat = () => {
    setModal({
      type: 'deleteChat',
      message: 'Вы уверены, что хотите удалить этот чат? Это действие нельзя отменить.',
      onConfirm: async () => {
        try {
          const response = await fetch(`${BASE_URL}/chats/delete/${chatId}`, {
            method: 'DELETE',
            headers: { Authorization: `Bearer ${token}` },
          });
          if (response.ok) {
            setModal({
              type: 'error',
              message: 'Чат успешно удалён!',
            });
            setTimeout(onBack, 1000);
          } else {
            const error = await response.json();
            setModal({
              type: 'error',
              message: `Ошибка: ${error.detail || 'Не удалось удалить чат'}`,
            });
          }
        } catch (err) {
          setModal({
            type: 'error',
            message: 'Ошибка сети при удалении чата.',
          });
        }
      },
    });
  };

  const handleContextMenu = (e: React.MouseEvent, messageId: number) => {
    e.preventDefault();
    const message = messages.find((m) => m.id === messageId);
    const isMine = message?.sender === username;
    const menuWidth = 150;
    const menuHeight = 150;
    let x = e.clientX;
    let y = e.clientY;
    if (x + menuWidth > window.innerWidth) x = window.innerWidth - menuWidth;
    if (y + menuHeight > window.innerHeight) y = window.innerHeight - menuHeight;
    setContextMenu({ x, y, messageId, isMine });
  };

  const handleEditMessage = (messageId: number) => {
    const message = messages.find((m) => m.id === messageId);
    if (message) {
      setEditingMessage(message);
      setMessageInput(message.content);
      setReplyTo(null);
    }
    setContextMenu(null);
  };

  const handleDeleteMessage = (messageId: number) => {
    setModal({
      type: 'deleteMessage',
      message: 'Вы уверены, что хотите удалить это сообщение?',
      onConfirm: () => {
        if (!wsRef.current || wsRef.current.readyState !== WebSocket.OPEN) return;
        const deleteData = { type: "delete", message_id: messageId };
        wsRef.current.send(JSON.stringify(deleteData));
        setContextMenu(null);
        setModal(null);
      },
    });
  };

  const handleCopyMessage = (messageId: number) => {
    const message = messages.find((m) => m.id === messageId);
    if (message) {
      navigator.clipboard.writeText(message.content);
      setModal({
        type: 'copy',
        message: 'Сообщение скопировано!',
      });
      setTimeout(() => setModal(null), 1000);
    }
    setContextMenu(null);
  };

  const handleReplyMessage = (messageId: number) => {
    const message = messages.find((m) => m.id === messageId);
    if (message) {
      setReplyTo(message);
      setMessageInput('');
      setEditingMessage(null);
    }
    setContextMenu(null);
  };

  const handleUserClick = (sender: string, isMine: boolean) => {
    if (!isMine && interlocutorDeleted) {
      setModal({
        type: 'deletedUser',
        message: 'Информация о удалённом аккаунте недоступна.',
      });
      setTimeout(() => setModal(null), 1500); // Автозакрытие через 1.5 сек
    } else {
      setSelectedUser(sender);
    }
  };

  const renderMessagesWithSeparators = () => {
    const result: React.ReactNode[] = [];
    let lastDateLabel: string | null = null;

    messages.forEach((msg) => {
      const currentDateLabel = formatDateLabel(msg.timestamp);

      if (currentDateLabel !== lastDateLabel) {
        result.push(
          <div key={`separator-${msg.id}`} className="text-center text-gray-500 py-2 border-b border-gray-300">
            {currentDateLabel}
          </div>
        );
        lastDateLabel = currentDateLabel;
      }

      const isMine = msg.sender === username;
      const isDeleted = msg.is_deleted === true;
      const displayName = !isMine && interlocutorDeleted ? 'Удалённый аккаунт' : msg.sender;
      const repliedMessage = msg.reply_to ? messages.find((m) => m.id === msg.reply_to) : null;

      result.push(
        <div
          key={msg.id}
          className={`mb-2 flex items-start max-w-[70%] break-words w-fit ${
            isMine ? 'ml-auto' : 'mr-auto'
          }`}
          onContextMenu={(e) => handleContextMenu(e, msg.id)}
        >
          {!isMine && (
            <div className="mr-2">
              <img
                src={`${BASE_URL}${msg.avatar_url || DEFAULT_AVATAR}`}
                alt={displayName}
                className={`w-8 h-8 rounded-full ${
                  interlocutorDeleted ? 'cursor-not-allowed opacity-50' : 'cursor-pointer'
                }`}
                onClick={() => handleUserClick(msg.sender, isMine)}
              />
            </div>
          )}
          <div
            className={`p-3 rounded-md ${
              isMine ? 'bg-blue-500 text-white' : 'bg-gray-200 text-black'
            }`}
          >
            {!isMine && (
              <div
                className={`font-semibold ${
                  interlocutorDeleted ? 'cursor-not-allowed opacity-50' : 'cursor-pointer'
                } ${isDeleted ? 'italic text-gray-500' : ''}`}
                onClick={() => handleUserClick(msg.sender, isMine)}
              >
                {displayName}
              </div>
            )}
            {repliedMessage ? (
              <div className="bg-gray-400 text-white opacity-70 p-2 rounded mb-2">
                {shortenText(repliedMessage.content)}
              </div>
            ) : msg.reply_to ? (
              <div className="bg-gray-400 text-white opacity-70 p-2 rounded mb-2">
                [Сообщение удалено]
              </div>
            ) : null}
            <div>{msg.content}</div>
            <div className={`text-xs opacity-50 ${isMine ? 'text-right' : 'text-left'}`}>
              {getTime(msg.timestamp)}
            </div>
          </div>
          {isMine && (
            <div className="ml-2">
              <img
                src={`${BASE_URL}${msg.avatar_url || DEFAULT_AVATAR}`}
                alt={msg.sender}
                className="w-8 h-8 rounded-full cursor-pointer"
                onClick={() => handleUserClick(msg.sender, isMine)}
              />
            </div>
          )}
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
      <div
        ref={chatWindowRef}
        className="flex-1 overflow-y-auto border border-gray-300 p-4 bg-gray-50 rounded"
      >
        {renderMessagesWithSeparators()}
      </div>
      <div className="flex pt-4 flex-col">
        {!interlocutorDeleted ? (
          <>
            {(replyTo || editingMessage) && (
              <div className="flex items-center mb-2">
                <span className="text-gray-500 mr-2">
                  {replyTo ? `Ответ на: ${shortenText(replyTo.content)}` : `Редактирование: ${shortenText(editingMessage!.content)}`}
                </span>
                <button
                  className="text-red-500 hover:text-red-700"
                  onClick={() => {
                    setReplyTo(null);
                    setEditingMessage(null);
                    setMessageInput('');
                  }}
                >
                  ✕
                </button>
              </div>
            )}
            <div className="flex">
              <input
                type="text"
                placeholder={editingMessage ? 'Отредактируйте сообщение' : 'Введите сообщение'}
                value={messageInput}
                onChange={(e) => setMessageInput(e.target.value)}
                className="flex-1 p-2 border border-gray-300 rounded-l focus:outline-none focus:ring-2 focus:ring-blue-500"
              />
              <button
                className="bg-blue-500 text-white p-2 rounded-r hover:bg-blue-600 transition-colors"
                onClick={handleSendMessage}
              >
                {editingMessage ? 'Сохранить' : 'Отправить'}
              </button>
            </div>
          </>
        ) : (
          <button
            className="w-full bg-red-500 text-white p-2 rounded hover:bg-red-600 transition-colors"
            onClick={handleDeleteChat}
          >
            Удалить чат
          </button>
        )}
      </div>
      {contextMenu && (
        <ContextMenuComponent
          ref={contextMenuRef}
          x={contextMenu.x}
          y={contextMenu.y}
          isMine={contextMenu.isMine}
          onEdit={() => handleEditMessage(contextMenu.messageId)}
          onDelete={() => handleDeleteMessage(contextMenu.messageId)}
          onCopy={() => handleCopyMessage(contextMenu.messageId)}
          onReply={() => handleReplyMessage(contextMenu.messageId)}
        />
      )}
      {selectedUser && (
        <UserProfileComponent username={selectedUser} onClose={() => setSelectedUser(null)} />
      )}
      {modal && (
        <ConfirmModal
          title={
            modal.type === 'deleteMessage'
              ? 'Удаление сообщения'
              : modal.type === 'deleteChat'
              ? 'Удаление чата'
              : modal.type === 'copy'
              ? 'Успех'
              : modal.type === 'deletedUser'
              ? 'Ошибка'
              : 'Ошибка'
          }
          message={modal.message}
          onConfirm={modal.onConfirm || (() => setModal(null))}
          onCancel={() => setModal(null)}
          confirmText={
            modal.type === 'copy' || modal.type === 'error' || modal.type === 'deletedUser'
              ? 'OK'
              : 'Подтвердить'
          }
          isError={modal.type === 'error' || modal.type === 'copy' || modal.type === 'deletedUser'}
        />
      )}
    </div>
  );
};

export default ChatComponent;