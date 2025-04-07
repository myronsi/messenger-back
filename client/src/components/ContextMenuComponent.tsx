import React from 'react';

interface ContextMenuComponentProps {
  x: number;
  y: number;
  onEdit: () => void;
  onDelete: () => void;
}

const ContextMenuComponent: React.FC<ContextMenuComponentProps> = ({ x, y, onEdit, onDelete }) => {
  return (
    <div
      className="absolute bg-white border border-gray-300 shadow-lg rounded p-2"
      style={{ top: y, left: x }}
    >
      <button
        className="block w-full text-left p-1 hover:bg-gray-100 rounded"
        onClick={onEdit}
      >
        Редактировать
      </button>
      <button
        className="block w-full text-left p-1 hover:bg-gray-100 rounded"
        onClick={onDelete}
      >
        Удалить
      </button>
    </div>
  );
};

export default ContextMenuComponent;