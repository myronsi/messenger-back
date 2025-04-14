import React, { useState, useEffect } from 'react';

interface UserProfileComponentProps {
  username: string;
  onClose: () => void;
}

const BASE_URL = "http://192.168.178.29:8000";
const DEFAULT_AVATAR = "/static/avatars/default.jpg";

const UserProfileComponent: React.FC<UserProfileComponentProps> = ({ username, onClose }) => {
  const [avatarUrl, setAvatarUrl] = useState(DEFAULT_AVATAR);
  const [bio, setBio] = useState('');
  const [isDeleted, setIsDeleted] = useState(false);
  const [isLoading, setIsLoading] = useState(true);

  useEffect(() => {
    const fetchUserProfile = async () => {
      try {
        console.log(`Запрос профиля: ${BASE_URL}/users/users/${username}`);
        const response = await fetch(`${BASE_URL}/users/users/${username}`);
        console.log(`Статус ответа: ${response.status}`);
        if (response.ok) {
          const data = await response.json();
          console.log('Данные:', data);
          if (data.is_deleted) {
            setIsDeleted(true);
          } else {
            setAvatarUrl(data.avatar_url || DEFAULT_AVATAR);
            setBio(data.bio || '');
          }
        } else if (response.status === 404) {
          // Считаем 404 как удалённый аккаунт
          setIsDeleted(true);
        } else {
          throw new Error(`HTTP ошибка: ${response.status}`);
        }
      } catch (err) {
        console.error('Ошибка при загрузке профиля:', err);
        setIsDeleted(true);
      } finally {
        setIsLoading(false);
      }
    };
    fetchUserProfile();
  }, [username]);

  if (isLoading) {
    return (
      <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center">
        <div className="bg-white p-6 rounded shadow-lg w-96">
          <p>Загрузка...</p>
        </div>
      </div>
    );
  }

  if (isDeleted) {
    return (
      <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center">
        <div className="bg-white p-6 rounded shadow-lg w-96">
          <h3 className="text-lg font-bold mb-4">Профиль пользователя</h3>
          <p className="text-gray-500 mb-4">Информация о удалённом аккаунте недоступна</p>
          <button
            onClick={onClose}
            className="w-full bg-gray-300 text-black p-2 rounded hover:bg-gray-400 transition-colors"
          >
            Закрыть
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center">
      <div className="bg-white p-6 rounded shadow-lg w-96">
        <h3 className="text-lg font-bold mb-4">Профиль пользователя</h3>
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
        <button
          onClick={onClose}
          className="w-full bg-gray-300 text-black p-2 rounded hover:bg-gray-400 transition-colors"
        >
          Закрыть
        </button>
      </div>
    </div>
  );
};

export default UserProfileComponent;