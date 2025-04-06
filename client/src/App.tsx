import React, { useState, useEffect } from 'react';
import RegisterComponent from './components/RegisterComponent';
import LoginComponent from './components/LoginComponent';
import ChatsListComponent from './components/ChatsListComponent';
import ChatComponent from './components/ChatComponent';
import './styles.css';

const App: React.FC = () => {
  const [isLoggedIn, setIsLoggedIn] = useState(false);
  const [username, setUsername] = useState('');
  const [currentChat, setCurrentChat] = useState<{ id: number; name: string } | null>(null);

  useEffect(() => {
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
        });
    }
  }, []);

  const handleLoginSuccess = (user: string) => {
    setIsLoggedIn(true);
    setUsername(user);
  };

  const handleLogout = () => {
    localStorage.removeItem('access_token');
    setIsLoggedIn(false);
    setUsername('');
    setCurrentChat(null);
  };

  const openChat = (chatId: number, chatName: string) => {
    setCurrentChat({ id: chatId, name: chatName });
  };

  const backToChats = () => {
    setCurrentChat(null);
  };

  return (
    <div id="app">
      {!isLoggedIn ? (
        <>
          <RegisterComponent />
          <LoginComponent onLoginSuccess={handleLoginSuccess} />
        </>
      ) : (
        <div className="app-container">
          <div className="chats-list">
            <ChatsListComponent username={username} onChatOpen={openChat} />
          </div>
          <div className="chat-area">
            {currentChat ? (
              <ChatComponent
                chatId={currentChat.id}
                chatName={currentChat.name}
                username={username}
                onBack={backToChats}
              />
            ) : (
              <p>Выберите чат</p>
            )}
          </div>
        </div>
      )}
      {isLoggedIn && <button id="logout-btn" onClick={handleLogout}>Log Out</button>}
    </div>
  );
};

export default App;