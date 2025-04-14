import React from 'react';

interface ConfirmModalProps {
  title: string;
  message: string;
  onConfirm: () => void;
  onCancel: () => void;
  confirmText?: string;
  cancelText?: string;
  isError?: boolean;
}

const ConfirmModal: React.FC<ConfirmModalProps> = ({
  title,
  message,
  onConfirm,
  onCancel,
  confirmText = 'Подтвердить',
  cancelText = 'Отменить',
  isError = false,
}) => {
  return (
    <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50">
      <div className="bg-white p-6 rounded shadow-lg w-96">
        <h3 className="text-lg font-bold mb-4">{title}</h3>
        <p className="text-gray-500 mb-4">{message}</p>
        <div className="flex justify-end space-x-2">
          {!isError && (
            <button
              onClick={onCancel}
              className="bg-gray-300 text-black p-2 rounded hover:bg-gray-400 transition-colors"
            >
              {cancelText}
            </button>
          )}
          <button
            onClick={isError ? onCancel : onConfirm}
            className={`p-2 rounded text-white transition-colors ${
              isError ? 'bg-red-500 hover:bg-red-600' : 'bg-blue-500 hover:bg-blue-600'
            }`}
          >
            {isError ? 'Закрыть' : confirmText}
          </button>
        </div>
      </div>
    </div>
  );
};

export default ConfirmModal;