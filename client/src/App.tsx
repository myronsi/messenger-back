import React, { useState, useEffect, useRef } from 'react';
import RegisterComponent from './components/RegisterComponent';
import LoginComponent from './components/LoginComponent';
import ChatsListComponent from './components/ChatsListComponent';
import ChatComponent from './components/ChatComponent';
import ProfileComponent from './components/ProfileComponent';
import './index.css';

const App: React.FC = () => {
  const [isLoggedIn, setIsLoggedIn] = useState(false);
  const [username, setUsername] = useState('');
  const [currentChat, setCurrentChat] = useState<{ id: number; name: string } | null>(null);
  const [isProfileOpen, setIsProfileOpen] = useState(false);
  const [isLoading, setIsLoading] = useState(true);
  const hasFetchedUser = useRef(false);

  useEffect(() => {
    if (hasFetchedUser.current) return;
    hasFetchedUser.current = true;

    const token = localStorage.getItem('access_token');
    if (token) {
      fetch('http://192.168.178.29:8000/auth/me', {
        headers: { Authorization: `Bearer ${token}` },
      })
        .then((response) => {
          if (response.ok) return response.json();
          throw new Error('Токен недействителен');
        })
        .then((user) => {
          setIsLoggedIn(true);
          setUsername(user.username);
        })
        .catch(() => {
          localStorage.removeItem('access_token');
          setIsLoggedIn(false);
          setUsername('');
        })
        .finally(() => {
          setIsLoading(false);
        });
    } else {
      setIsLoading(false);
    }
  }, []);

  const handleLoginSuccess = (user: string) => {
    setIsLoggedIn(true);
    setUsername(user);
    setIsLoading(false);
  };

  const handleLogout = () => {
    localStorage.removeItem('access_token');
    setIsLoggedIn(false);
    setUsername('');
    setCurrentChat(null);
    setIsProfileOpen(false);
  };

  const openChat = (chatId: number, chatName: string) => {
    console.log('Открываем чат:', chatId, chatName); // Лог для отладки
    setCurrentChat({ id: chatId, name: chatName });
  };

  const backToChats = () => {
    setCurrentChat(null);
  };

  if (isLoading) {
    return (
      <div className="h-screen flex items-center justify-center">
        <p className="text-gray-500 text-lg">Загрузка...</p>
      </div>
    );
  }

  return (
    <div className="h-screen flex flex-col">
      {!isLoggedIn ? (
        <div className="flex flex-col items-center justify-center h-full space-y-4">
          <RegisterComponent onLoginSuccess={handleLoginSuccess} />
          <LoginComponent onLoginSuccess={handleLoginSuccess} />
        </div>
      ) : (
        <>
          <div className="flex flex-1 overflow-hidden">
            <div className="w-1/5 border-r border-gray-300 overflow-y-auto p-4">
              <ChatsListComponent
                username={username}
                onChatOpen={openChat}
                setIsProfileOpen={setIsProfileOpen}
                activeChatId={currentChat?.id} // Передаём ID активного чата
              />
            </div>
            <div className="w-4/5 flex justify-center items-center p-4">
              {currentChat ? (
                <ChatComponent
                  key={currentChat.id} // Уникальный ключ для принудительного перерендера
                  chatId={currentChat.id}
                  chatName={currentChat.name}
                  username={username}
                  onBack={backToChats}
                />
              ) : (
                <p className="text-gray-500 text-lg">Выберите чат</p>
              )}
            </div>
          </div>
          {isProfileOpen && <ProfileComponent onClose={() => setIsProfileOpen(false)} />}
        </>
      )}
      {isLoggedIn && (
        <button
          className="bg-red-500 text-white p-2 mt-4 mx-4 rounded hover:bg-red-600 transition-colors"
          onClick={handleLogout}
        >
          Выйти
        </button>
      )}
    </div>
  );
};

export default App;