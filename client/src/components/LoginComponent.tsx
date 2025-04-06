import React, { useState } from 'react';

interface LoginComponentProps {
  onLoginSuccess: (username: string) => void;
}

const LoginComponent: React.FC<LoginComponentProps> = ({ onLoginSuccess }) => {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [message, setMessage] = useState('');

  const handleLogin = async () => {
    try {
      const response = await fetch('http://192.168.178.29:8000/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password }),
      });
      const data = await response.json();
      if (response.ok) {
        localStorage.setItem('access_token', data.access_token);
        onLoginSuccess(username);
      } else {
        setMessage(data.detail);
      }
    } catch (err) {
      setMessage('Ошибка сети. Проверьте подключение.');
    }
  };

  return (
    <div id="login-section">
      <h2>Log In</h2>
      <input
        id="login-username"
        type="text"
        placeholder="Username"
        value={username}
        onChange={(e) => setUsername(e.target.value)}
      />
      <input
        id="login-password"
        type="password"
        placeholder="Password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
      />
      <button id="login-btn" onClick={handleLogin}>Log In</button>
      <p id="login-message">{message}</p>
    </div>
  );
};

export default LoginComponent;