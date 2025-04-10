export interface Chat {
    id: number;
    name: string;
  }
  
  export interface Message {
    id: number;
    sender: string;
    avatar_url: string | null;
    content: string;
    timestamp: string;
  }
  
  export interface Chat {
    id: number;
    name: string;
    avatar_url: string | null;
  }