import React, { useState, useEffect, forwardRef } from 'react';
import ConfirmModal from './ConfirmModal';

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
  const [modal, setModal] = useState<{
    type: 'deleteAccount' | 'error' | 'success';
    message: string;
    onConfirm?: () => void;
  } | null>(null);
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
        setModal({
          type: 'error',
          message: 'Не удалось загрузить профиль. Попробуйте снова.',
        });
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
        setModal({
          type: 'success',
          message: 'Аватарка успешно обновлена!',
        });
        setAvatarFile(null);
      } else {
        setModal({
          type: 'error',
          message: data.detail || 'Ошибка при загрузке аватарки.',
        });
      }
    } catch (err) {
      setModal({
        type: 'error',
        message: 'Ошибка сети при загрузке аватарки.',
      });
    } finally {
      setIsUploading(false);
    }
  };

  const handleDeleteAccount = () => {
    setModal({
      type: 'deleteAccount',
      message: 'Вы уверены, что хотите удалить аккаунт? Это действие будет выполнено мгновенно и его нельзя отменить.',
      onConfirm: async () => {
        try {
          const response = await fetch(`${BASE_URL}/auth/me`, {
            method: 'DELETE',
            headers: { Authorization: `Bearer ${token}` },
          });
          if (response.ok) {
            setModal({
              type: 'success',
              message: 'Аккаунт успешно удалён!',
            });
            localStorage.removeItem('access_token');
            setTimeout(() => {
              onClose();
              window.location.reload();
            }, 1000);
          } else {
            const error = await response.json();
            setModal({
              type: 'error',
              message: `Ошибка: ${error.detail || 'Не удалось удалить аккаунт'}`,
            });
          }
        } catch (err) {
          setModal({
            type: 'error',
            message: 'Ошибка сети при удалении аккаунта.',
          });
        }
      },
    });
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
      {modal && (
        <ConfirmModal
          title={
            modal.type === 'deleteAccount'
              ? 'Удаление аккаунта'
              : modal.type === 'success'
              ? 'Успех'
              : 'Ошибка'
          }
          message={modal.message}
          onConfirm={modal.onConfirm || (() => setModal(null))}
          onCancel={() => setModal(null)}
          confirmText={modal.type === 'success' || modal.type === 'error' ? 'OK' : 'Подтвердить'}
          isError={modal.type === 'success' || modal.type === 'error'}
        />
      )}
    </div>
  );
});

export default ProfileComponent;