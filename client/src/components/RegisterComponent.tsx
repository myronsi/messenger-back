import React, { useState } from 'react';

const RegisterComponent: React.FC = () => {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [message, setMessage] = useState('');

  const handleRegister = async () => {
    try {
      const response = await fetch('http://192.168.178.29:8000/auth/register', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password }),
      });
      const data = await response.json();
      setMessage(data.message || data.detail);
    } catch (err) {
      setMessage('Ошибка сети. Проверьте подключение.');
    }
  };

  return (
    <div id="register-section">
      <h2>Sign In</h2>
      <input
        id="register-username"
        type="text"
        placeholder="Username"
        value={username}
        onChange={(e) => setUsername(e.target.value)}
      />
      <input
        id="register-password"
        type="password"
        placeholder="Password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
      />
      <button id="register-btn" onClick={handleRegister}>Sign In</button>
      <p id="register-message">{message}</p>
    </div>
  );
};

export default RegisterComponent;