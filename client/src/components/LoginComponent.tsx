import React, { useState } from 'react';

interface LoginComponentProps {
  onLoginSuccess: (username: string) => void;
}

const BASE_URL = "http://192.168.178.29:8000";

const LoginComponent: React.FC<LoginComponentProps> = ({ onLoginSuccess }) => {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [message, setMessage] = useState('');

  const handleLogin = async () => {
    try {
      const response = await fetch(`${BASE_URL}/auth/login`, {
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
    <div id="login-section" className="space-y-4">
      <h2 className="text-xl font-bold">Log In</h2>
      <input
        id="login-username"
        type="text"
        placeholder="Username"
        value={username}
        onChange={(e) => setUsername(e.target.value)}
        className="w-full p-2 border border-gray-300 rounded"
      />
      <input
        id="login-password"
        type="password"
        placeholder="Password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        className="w-full p-2 border border-gray-300 rounded"
      />
      <button
        id="login-btn"
        onClick={handleLogin}
        className="w-full bg-blue-500 text-white p-2 rounded hover:bg-blue-600 transition-colors"
      >
        Log In
      </button>
      <p id="login-message" className="text-red-500">{message}</p>
    </div>
  );
};

export default LoginComponent;