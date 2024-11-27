const BASE_URL = "http://127.0.0.1:8000";
let currentChatId = null;
let ws = null;

// Регистрация
document.getElementById("register-btn").onclick = async () => {
    const username = document.getElementById("register-username").value;
    const password = document.getElementById("register-password").value;
    const response = await fetch(`${BASE_URL}/auth/register`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username, password })
    });

    const message = await response.json();
    document.getElementById("register-message").textContent = message.message || message.detail;
};

// Авторизация
document.getElementById("login-btn").onclick = async () => {
    const username = document.getElementById("login-username").value;
    const password = document.getElementById("login-password").value;
    const response = await fetch(`${BASE_URL}/auth/login`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username, password })
    });

    const message = await response.json();
    if (response.ok) {
        document.getElementById("login-message").textContent = "Успешный вход!";
        initChats(username);
    } else {
        document.getElementById("login-message").textContent = message.detail;
    }
};

// Инициализация чатов
async function initChats(username) {
    document.getElementById("login-section").style.display = "none";
    document.getElementById("register-section").style.display = "none";
    document.getElementById("chats-section").style.display = "block";

    const response = await fetch(`${BASE_URL}/chats/list/${username}`);
    const { chats } = await response.json();

    const chatsList = document.getElementById("chats-list");
    chatsList.innerHTML = ""; // Очистка списка
    if (Array.isArray(chats)) {
        chats.forEach(chat => {
            const chatItem = document.createElement("div");
            chatItem.className = "chat-item";
            chatItem.textContent = chat.name;
            chatItem.onclick = () => openChat(chat.id, chat.name, username);
            chatsList.appendChild(chatItem);
        });
    } else {
        console.error("Ошибка: chats не является массивом", chats);
    }
    

    document.getElementById("create-chat-btn").onclick = async () => {
        const targetUser = document.getElementById("chat-username").value;
        const response = await fetch(`${BASE_URL}/chats/create`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ user1: username, user2: targetUser })
        });

        const message = await response.json();
        if (response.ok) {
            alert("Чат создан!");
            initChats(username);
        } else {
            alert(message.detail);
        }
    };
}

// Открытие чата
async function openChat(chatId, chatName, username) {
    document.getElementById("chats-section").style.display = "none";
    document.getElementById("chat-section").style.display = "block";
    document.getElementById("chat-name").textContent = chatName;
    currentChatId = chatId;

    const response = await fetch(`${BASE_URL}/messages/history/${chatId}`);
    const { history } = await response.json();

    const chatWindow = document.getElementById("chat-window");
    chatWindow.innerHTML = "";
    history.forEach(msg => {
        const msgDiv = document.createElement("div");
        msgDiv.textContent = `${msg.timestamp} - ${msg.sender}: ${msg.content}`;
        chatWindow.appendChild(msgDiv);
    });

    ws = new WebSocket(`ws://127.0.0.1:8000/ws/${username}`);
    ws.onmessage = (event) => {
        const messageDiv = document.createElement("div");
        messageDiv.textContent = event.data;
        chatWindow.appendChild(messageDiv);
    };

    document.getElementById("send-btn").onclick = () => {
        const messageInput = document.getElementById("message-input");
        const message = `${currentChatId}:${messageInput.value}`; // Формат chat_id:сообщение
        ws.send(message);
        messageInput.value = "";
    };
}
