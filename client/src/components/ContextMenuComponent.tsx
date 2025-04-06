import React from 'react';

interface ContextMenuComponentProps {
  x: number;
  y: number;
  onEdit: () => void;
  onDelete: () => void;
}

const ContextMenuComponent: React.FC<ContextMenuComponentProps> = ({ x, y, onEdit, onDelete }) => {
  return (
    <div id="context-menu" style={{ top: `${y}px`, left: `${x}px` }}>
      <button id="edit-btn" onClick={onEdit}>Edit</button>
      <button id="delete-btn" onClick={onDelete}>Delete</button>
    </div>
  );
};

export default ContextMenuComponent;