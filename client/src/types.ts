export interface Chat {
    id: number;
    name: string;
  }
  
  export interface Message {
    id: number;
    sender: string;
    content: string;
    timestamp: string;
    avatar_url?: string;
    reply_to?: number | null; // ID сообщения, на которое это ответ
  }
  
  export interface Chat {
    id: number;
    name: string;
    avatar_url: string | null;
  }