import React, { useState, useEffect, forwardRef } from 'react';

interface ProfileComponentProps {
  onClose: () => void;
}

const BASE_URL = "http://192.168.178.29:8000";
const DEFAULT_AVATAR = "/static/avatars/default.jpg";

const ProfileComponent = forwardRef<HTMLDivElement, ProfileComponentProps>(({ onClose }, ref) => {
  const [username, setUsername] = useState('');
  const [avatarUrl, setAvatarUrl] = useState(DEFAULT_AVATAR);
  const [bio, setBio] = useState('');
  const [avatarFile, setAvatarFile] = useState<File | null>(null);
  const [isUploading, setIsUploading] = useState(false);
  const token = localStorage.getItem('access_token');

  useEffect(() => {
    const fetchProfile = async () => {
      try {
        const response = await fetch(`${BASE_URL}/auth/me`, {
          headers: { Authorization: `Bearer ${token}` },
        });
        if (response.ok) {
          const data = await response.json();
          setUsername(data.username);
          setAvatarUrl(data.avatar_url || DEFAULT_AVATAR);
          setBio(data.bio || '');
        } else {
          throw new Error('Ошибка загрузки профиля');
        }
      } catch (err) {
        console.error('Ошибка при загрузке профиля:', err);
        alert('Не удалось загрузить профиль. Попробуйте снова.');
      }
    };
    if (token) fetchProfile();
  }, [token]);

  const handleAvatarUpload = async () => {
    if (!avatarFile || !token) return;

    setIsUploading(true);
    try {
      const formData = new FormData();
      formData.append('file', avatarFile);

      const response = await fetch(`${BASE_URL}/auth/me/avatar`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: formData,
      });
      const data = await response.json();
      if (response.ok) {
        setAvatarUrl(data.avatar_url);
        alert('Аватарка успешно обновлена!');
        setAvatarFile(null);
      } else {
        alert(data.detail || 'Ошибка при загрузке аватарки');
      }
    } catch (err) {
      alert('Ошибка сети при загрузке аватарки. Проверьте подключение.');
    } finally {
      setIsUploading(false);
    }
  };

  const handleDeleteAccount = async () => {
    if (!window.confirm('Вы уверены, что хотите удалить аккаунт? Это действие нельзя отменить.')) return;
  
    try {
      const response = await fetch(`${BASE_URL}/auth/me`, {
        method: 'DELETE',
        headers: { Authorization: `Bearer ${token}` },
      });
      if (response.ok) {
        alert('Аккаунт удалён!');
        localStorage.removeItem('access_token');
        onClose(); // Закрываем профиль
        window.location.reload(); // Перезагружаем страницу для сброса состояния
      } else {
        const error = await response.json();
        alert(`Ошибка: ${error.detail || 'Не удалось удалить аккаунт'}`);
      }
    } catch (err) {
      alert('Ошибка сети при удалении аккаунта. Проверьте подключение.');
    }
  };

  return (
    <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center">
      <div ref={ref} className="bg-white p-6 rounded shadow-lg w-96">
        <h3 className="text-lg font-bold mb-4">Профиль</h3>
        <div className="flex items-center mb-4">
          <img
            src={`${BASE_URL}${avatarUrl}`}
            alt={username}
            className="w-16 h-16 rounded-full mr-4"
          />
          <div>
            <div className="font-semibold">{username}</div>
            <div className="text-gray-500">{bio || 'Био не указано'}</div>
          </div>
        </div>
        <div className="space-y-4">
          <div>
            <label className="block text-sm font-medium mb-1">Обновить аватарку</label>
            <input
              type="file"
              accept="image/*"
              onChange={(e) => setAvatarFile(e.target.files?.[0] || null)}
              className="w-full"
              disabled={isUploading}
            />
            <button
              onClick={handleAvatarUpload}
              disabled={!avatarFile || isUploading}
              className={`w-full mt-2 p-2 rounded text-white transition-colors ${
                avatarFile && !isUploading ? 'bg-blue-500 hover:bg-blue-600' : 'bg-gray-500 cursor-not-allowed'
              }`}
            >
              {isUploading ? 'Загрузка...' : 'Загрузить'}
            </button>
          </div>
          <button
            onClick={handleDeleteAccount}
            className="w-full bg-red-500 text-white p-2 rounded hover:bg-red-600 transition-colors"
          >
            Удалить аккаунт
          </button>
          <button
            onClick={onClose}
            className="w-full bg-gray-300 text-black p-2 rounded hover:bg-gray-400 transition-colors"
          >
            Закрыть
          </button>
        </div>
      </div>
    </div>
  );
});

export default ProfileComponent;