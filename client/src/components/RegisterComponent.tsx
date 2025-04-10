import React, { useState } from 'react';

interface RegisterComponentProps {
  onLoginSuccess: (username: string) => void;
}

const BASE_URL = "http://192.168.178.29:8000";

const RegisterComponent: React.FC<RegisterComponentProps> = ({ onLoginSuccess }) => {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [message, setMessage] = useState('');
  const [isModalOpen, setIsModalOpen] = useState(false);
  const [avatarFile, setAvatarFile] = useState<File | null>(null);
  const [bio, setBio] = useState('');
  const [token, setToken] = useState<string | null>(null);

  const handleRegister = async () => {
    try {
      const response = await fetch(`${BASE_URL}/auth/register`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password, bio: '' }),
      });
      const data = await response.json();
      if (response.ok) {
        setToken(data.access_token);
        localStorage.setItem('access_token', data.access_token);
        setMessage('Регистрация успешна! Теперь настройте профиль.');
        setIsModalOpen(true);
      } else {
        setMessage(data.detail);
      }
    } catch (err) {
      setMessage('Ошибка сети. Проверьте подключение.');
    }
  };

  const handleProfileSetup = async () => {
    try {
      if (avatarFile && token) {
        const formData = new FormData();
        formData.append('file', avatarFile);
        const avatarResponse = await fetch(`${BASE_URL}/auth/me/avatar`, {
          method: 'POST',
          headers: { Authorization: `Bearer ${token}` },
          body: formData,
        });
        if (!avatarResponse.ok) {
          const errorData = await avatarResponse.json();
          setMessage(`Ошибка загрузки аватарки: ${errorData.detail}`);
          return;
        }
      }

      if (bio && token) {
        const bioResponse = await fetch(`${BASE_URL}/auth/me`, {
          method: 'PUT',
          headers: {
            'Content-Type': 'application/json',
            Authorization: `Bearer ${token}`,
          },
          body: JSON.stringify({ bio }),
        });
        if (!bioResponse.ok) {
          const errorData = await bioResponse.json();
          setMessage(`Ошибка обновления bio: ${errorData.detail}`);
          return;
        }
      }

      setMessage('Профиль успешно настроен!');
      setIsModalOpen(false);
      setAvatarFile(null);
      setBio('');
      onLoginSuccess(username);
    } catch (err) {
      setMessage('Ошибка сети при настройке профиля.');
    }
  };

  const handleSkip = () => {
    setIsModalOpen(false);
    setMessage('Регистрация завершена.');
    onLoginSuccess(username);
  };

  return (
    <div id="register-section" className="space-y-4">
      <h2 className="text-xl font-bold">Sign In</h2>
      <input
        id="register-username"
        type="text"
        placeholder="Username"
        value={username}
        onChange={(e) => setUsername(e.target.value)}
        className="w-full p-2 border border-gray-300 rounded"
      />
      <input
        id="register-password"
        type="password"
        placeholder="Password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        className="w-full p-2 border border-gray-300 rounded"
      />
      <button
        id="register-btn"
        onClick={handleRegister}
        className="w-full bg-blue-500 text-white p-2 rounded hover:bg-blue-600 transition-colors"
      >
        Sign In
      </button>
      <p id="register-message" className="text-red-500">{message}</p>

      {isModalOpen && (
        <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center">
          <div className="bg-white p-6 rounded shadow-lg w-96 space-y-4">
            <h3 className="text-lg font-bold">Настройте ваш профиль</h3>
            <input
              type="file"
              accept="image/*"
              onChange={(e) => setAvatarFile(e.target.files?.[0] || null)}
              className="w-full"
            />
            <textarea
              placeholder="Описание профиля (опционально)"
              value={bio}
              onChange={(e) => setBio(e.target.value)}
              className="w-full p-2 border border-gray-300 rounded"
            />
            <div className="flex justify-end space-x-2">
              <button
                onClick={handleSkip}
                className="bg-gray-300 text-black p-2 rounded hover:bg-gray-400 transition-colors"
              >
                Пропустить
              </button>
              <button
                onClick={handleProfileSetup}
                className="bg-blue-500 text-white p-2 rounded hover:bg-blue-600 transition-colors"
              >
                Сохранить
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};

export default RegisterComponent;