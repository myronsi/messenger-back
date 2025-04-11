import React, { forwardRef } from 'react';

interface ContextMenuProps {
  x: number;
  y: number;
  isMine: boolean; // Добавляем флаг, чтобы различать свои и чужие сообщения
  onEdit: () => void;
  onDelete: () => void;
  onCopy: () => void;
  onReply: () => void;
}

const ContextMenuComponent = forwardRef<HTMLDivElement, ContextMenuProps>(
  ({ x, y, isMine, onEdit, onDelete, onCopy, onReply }, ref) => {
    return (
      <div
        ref={ref}
        className="absolute bg-white border border-gray-300 shadow-lg rounded p-2 z-10"
        style={{ top: y, left: x }}
      >
        {isMine && (
          <>
            <button
              className="block w-full text-left px-2 py-1 hover:bg-gray-100"
              onClick={onEdit}
            >
              Редактировать
            </button>
            <button
              className="block w-full text-left px-2 py-1 hover:bg-gray-100"
              onClick={onDelete}
            >
              Удалить
            </button>
          </>
        )}
        <button
          className="block w-full text-left px-2 py-1 hover:bg-gray-100"
          onClick={onCopy}
        >
          Скопировать
        </button>
        <button
          className="block w-full text-left px-2 py-1 hover:bg-gray-100"
          onClick={onReply}
        >
          Ответить
        </button>
      </div>
    );
  }
);

export default ContextMenuComponent;