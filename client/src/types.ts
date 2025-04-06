export interface Chat {
    id: number;
    name: string;
  }
  
  export interface Message {
    id: number;
    sender: string;
    content: string;
    timestamp: string;
  }