const BASE_URL = "http://192.168.178.29:8000";
const WS_URL = "ws://192.168.178.29:8000";
let currentChatId = null;
let ws = null;
let selectedMessageId = null;

// Проверка авторизации при загрузке страницы
window.onload = async () => {
    const token = localStorage.getItem("access_token");
    if (token) {
        try {
            const response = await fetch(`${BASE_URL}/auth/me`, {
                headers: { Authorization: `Bearer ${token}` },
            });
            if (response.ok) {
                const user = await response.json();
                console.log("Пользователь авторизован:", user.username);
                initChats(user.username);
            } else {
                console.log("Токен недействителен, требуется повторный вход");
                localStorage.removeItem("access_token");
                document.getElementById("login-section").style.display = "block";
            }
        } catch (err) {
            console.error("Ошибка при проверке токена:", err);
        }
    } else {
        console.log("Требуется авторизация");
        document.getElementById("login-section").style.display = "block";
    }
};

// Регистрация
document.getElementById("register-btn").onclick = async () => {
    const username = document.getElementById("register-username").value;
    const password = document.getElementById("register-password").value;
    const response = await fetch(`${BASE_URL}/auth/register`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username, password }),
    });

    const message = await response.json();
    document.getElementById("register-message").textContent = message.message || message.detail;
};

// Авторизация
async function login() {
    const username = document.getElementById("login-username").value;
    const password = document.getElementById("login-password").value;
    const response = await fetch(`${BASE_URL}/auth/login`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username, password }),
    });
    const message = await response.json();
    if (response.ok) {
        document.getElementById("logout-btn").style.display = "block";
        localStorage.setItem("access_token", message.access_token);
        initChats(username);
    } else {
        document.getElementById("login-message").textContent = message.detail;
    }
}

document.getElementById("login-btn").onclick = login;

// Логаут
document.getElementById("logout-btn").onclick = () => {
    localStorage.removeItem("access_token");
    document.getElementById("login-section").style.display = "block";
    document.getElementById("register-section").style.display = "block";
    document.getElementById("chats-section").style.display = "none";
    document.getElementById("logout-btn").style.display = "none";
};

// Инициализация списка чатов
async function initChats(username) {
    document.getElementById("login-section").style.display = "none";
    document.getElementById("register-section").style.display = "none";
    document.getElementById("chats-section").style.display = "block";

    const response = await fetch(`${BASE_URL}/chats/list/${username}`, {
        headers: { Authorization: `Bearer ${localStorage.getItem("access_token")}` },
    });
    const { chats } = await response.json();

    const chatsList = document.getElementById("chats-list");
    chatsList.innerHTML = "";
    if (Array.isArray(chats)) {
        chats.forEach((chat) => {
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
            headers: {
                "Content-Type": "application/json",
                Authorization: `Bearer ${localStorage.getItem("access_token")}`,
            },
            body: JSON.stringify({ user1: username, user2: targetUser }),
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

// Загрузка сообщений чата
async function loadChatMessages(chatId) {
    try {
        const response = await fetch(`${BASE_URL}/messages/history/${chatId}`, {
            headers: {
                Authorization: `Bearer ${localStorage.getItem("access_token")}`,
            },
        });

        if (response.ok) {
            const { history } = await response.json();
            const chatWindow = document.getElementById("chat-window");
            chatWindow.innerHTML = "";
            history.forEach((msg) => {
                const msgDiv = document.createElement("div");
                msgDiv.className = "message";
                msgDiv.dataset.messageId = msg.id;
                msgDiv.innerHTML = `<span>${msg.timestamp} - ${msg.sender}: ${msg.content}</span>`;
                chatWindow.appendChild(msgDiv);
            });
        } else if (response.status === 401) {
            alert("Сессия истекла. Пожалуйста, войдите снова.");
            localStorage.removeItem("access_token");
            window.location.reload();
        } else {
            console.error("Ошибка при загрузке сообщений:", await response.json());
        }
    } catch (err) {
        console.error("Ошибка сети при загрузке сообщений:", err);
    }
}

// Отправка сообщения через WebSocket
async function sendMessage() {
    const messageInput = document.getElementById("message-input");
    const content = messageInput.value.trim();
    if (!content || !ws || ws.readyState !== WebSocket.OPEN) return;

    const messageData = {
        chat_id: currentChatId,
        content: content,
    };

    ws.send(JSON.stringify(messageData));
    messageInput.value = "";
    // Локальное добавление сообщения удалено, ждём ответа от сервера
}

// Добавление сообщения в интерфейс
function addMessageToChat(username, content, messageId, timestamp) {
    const chatWindow = document.getElementById("chat-window");
    const msgDiv = document.createElement("div");
    msgDiv.className = "message";
    msgDiv.dataset.messageId = messageId; // ID теперь всегда присутствует
    msgDiv.innerHTML = `<span>${timestamp} - ${username}: ${content}</span>`;
    chatWindow.appendChild(msgDiv);
    chatWindow.scrollTop = chatWindow.scrollHeight;
}

// Открытие чата
async function openChat(chatId, chatName, username) {
    currentChatId = chatId;
    document.getElementById("chats-section").style.display = "none";
    document.getElementById("chat-section").style.display = "block";
    document.getElementById("chat-name").textContent = chatName;

    await loadChatMessages(chatId);

    const chatWindow = document.getElementById("chat-window");
    const menu = document.getElementById("context-menu");

    // Обработчик контекстного меню
    const contextMenuHandler = (event) => {
        event.preventDefault();
        const messageElement = event.target.closest(".message");
        if (!messageElement) return;
        selectedMessageId = messageElement.dataset.messageId;
        console.log("Выбранный ID сообщения:", selectedMessageId); // Для отладки
        if (!selectedMessageId) {
            alert("ID сообщения не найден");
            return;
        }

        const menuWidth = menu.offsetWidth;
        const menuHeight = menu.offsetHeight;
        let x = event.clientX;
        let y = event.clientY;

        if (x + menuWidth > window.innerWidth) x = window.innerWidth - menuWidth;
        if (y + menuHeight > window.innerHeight) y = window.innerHeight - menuHeight;

        menu.style.top = `${y}px`;
        menu.style.left = `${x}px`;
        menu.classList.remove("hidden");
    };

    // Скрытие меню при клике вне его
    const hideMenuHandler = (event) => {
        if (!menu.contains(event.target)) {
            menu.classList.add("hidden");
        }
    };

    chatWindow.addEventListener("contextmenu", contextMenuHandler);
    document.addEventListener("click", hideMenuHandler);

    // Редактирование сообщения
    document.getElementById("edit-btn").onclick = () => {
        if (!selectedMessageId) {
            alert("Сообщение не выбрано");
            return;
        }
        const contentElement = document.querySelector(`[data-message-id='${selectedMessageId}'] span`);
        editMessage(selectedMessageId, contentElement);
        menu.classList.add("hidden");
    };

    // Удаление сообщения
    document.getElementById("delete-btn").onclick = () => {
        if (!selectedMessageId) {
            alert("Сообщение не выбрано");
            return;
        }
        const messageElement = document.querySelector(`[data-message-id='${selectedMessageId}']`);
        deleteMessage(selectedMessageId, messageElement);
        menu.classList.add("hidden");
    };

    // Возврат к списку чатов
    document.getElementById("back-to-chats-btn").onclick = () => {
        chatWindow.removeEventListener("contextmenu", contextMenuHandler);
        document.removeEventListener("click", hideMenuHandler);
        document.getElementById("chat-section").style.display = "none";
        document.getElementById("chats-section").style.display = "block";
        document.getElementById("logout-btn").style.display = "block";
        currentChatId = null;
        if (ws) ws.close();
    };

    // Удаление чата
    document.getElementById("delete-chat-btn").onclick = async () => {
        const confirmDelete = confirm("Вы уверены, что хотите удалить этот чат?");
        if (!confirmDelete) return;

        try {
            const response = await fetch(`${BASE_URL}/chats/delete/${chatId}`, {
                method: "DELETE",
                headers: { Authorization: `Bearer ${localStorage.getItem("access_token")}` },
            });

            if (response.ok) {
                alert("Чат успешно удалён!");
                document.getElementById("chat-section").style.display = "none";
                document.getElementById("chats-section").style.display = "block";
                initChats(username);
            } else {
                const error = await response.json();
                alert(`Ошибка: ${error.detail}`);
            }
        } catch (err) {
            alert("Ошибка сети. Проверьте подключение к серверу.");
        }
    };

    // WebSocket подключение
    if (ws) ws.close();
    const token = localStorage.getItem("access_token");
    ws = new WebSocket(`${WS_URL}/ws/chat/${chatId}?token=${token}`);
    ws.onopen = () => console.log("WebSocket подключён к чату", chatId);
    ws.onmessage = (event) => {
        const parsedData = JSON.parse(event.data);
        const { username, data, timestamp } = parsedData;
        addMessageToChat(username, data.content, data.message_id, timestamp);
    };
    ws.onerror = (error) => console.error("WebSocket ошибка:", error);
    ws.onclose = () => console.log("WebSocket отключён");

    document.getElementById("send-btn").onclick = sendMessage;
}

// Редактирование сообщения
async function editMessage(messageId, contentElement) {
    const newContent = prompt("Введите новое сообщение:", contentElement.textContent.split(": ")[1]);
    if (!newContent) return;

    try {
        const response = await fetch(`${BASE_URL}/messages/edit/${messageId}`, {
            method: "PUT",
            headers: {
                "Content-Type": "application/json",
                Authorization: `Bearer ${localStorage.getItem("access_token")}`,
            },
            body: JSON.stringify({ content: newContent }),
        });

        if (response.ok) {
            alert("Сообщение обновлено!");
            contentElement.textContent = contentElement.textContent.split(": ")[0] + `: ${newContent}`;
        } else {
            const error = await response.json();
            alert(`Ошибка: ${error.detail}`);
        }
    } catch (err) {
        alert("Ошибка сети. Проверьте подключение к серверу.");
    }
}

// Удаление сообщения
async function deleteMessage(messageId, messageElement) {
    const confirmDelete = confirm("Вы уверены, что хотите удалить это сообщение?");
    if (!confirmDelete) return;

    try {
        const response = await fetch(`${BASE_URL}/messages/delete/${messageId}`, {
            method: "DELETE",
            headers: {
                Authorization: `Bearer ${localStorage.getItem("access_token")}`,
            },
        });

        if (response.ok) {
            alert("Сообщение удалено!");
            messageElement.remove();
        } else {
            const error = await response.json();
            alert(`Ошибка: ${error.detail}`);
        }
    } catch (err) {
        alert("Ошибка сети. Проверьте подключение к серверу.");
    }
}