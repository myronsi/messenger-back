export interface Chat {
  id: number;
  name: string;
  interlocutor_name: string;
  avatar_url: string;
  interlocutor_deleted: boolean;
}

export interface Message {
  id: number;
  sender: string;
  content: string;
  timestamp: string;
  avatar_url?: string;
  reply_to?: number | null;
  is_deleted?: boolean;
}