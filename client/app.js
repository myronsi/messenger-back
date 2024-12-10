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
    let selectedMessageId = null; // Хранение ID выбранного сообщения

    // Показ контекстного меню
    document.getElementById("chat-window").addEventListener("contextmenu", (event) => {
        event.preventDefault();
        console.log("Контекстное меню вызвано");

        const messageElement = event.target.closest(".message");
        if (!messageElement) {
            console.log("Не найдено сообщение под курсором");
            return;
        }

        selectedMessageId = messageElement.dataset.messageId;
        console.log(`Выбрано сообщение с ID: ${selectedMessageId}`);

        const menu = document.getElementById("context-menu");
        let x = event.clientX;
        let y = event.clientY;

        // Ограничиваем меню, чтобы не выходило за границы экрана
        const menuWidth = menu.offsetWidth;
        const menuHeight = menu.offsetHeight;

        if (x + menuWidth > window.innerWidth) {
            x = window.innerWidth - menuWidth;
        }
        if (y + menuHeight > window.innerHeight) {
            y = window.innerHeight - menuHeight;
        }
    
        menu.style.top = `${y}px`;
        menu.style.left = `${x}px`;
        menu.classList.remove("hidden");
    });

    // Скрытие меню при клике вне его
    document.addEventListener("click", (event) => {
        const menu = document.getElementById("context-menu");

        if (!menu.contains(event.target)) {
            console.log("Клик вне контекстного меню, скрываем меню");
            menu.classList.add("hidden");
        }
    });
    
    // Обработка кнопки "Редактировать"
    document.getElementById("edit-btn").addEventListener("click", () => {
        const contentElement = document.querySelector(`[data-message-id='${selectedMessageId}'] span`);
        editMessage(selectedMessageId, contentElement);
    
        // Скрыть меню после действия
        document.getElementById("context-menu").classList.add("hidden");
    });
    
    // Обработка кнопки "Удалить"
    document.getElementById("delete-btn").addEventListener("click", () => {
        const messageElement = document.querySelector(`[data-message-id='${selectedMessageId}']`);
        deleteMessage(selectedMessageId, messageElement);
    
        // Скрыть меню после действия
        document.getElementById("context-menu").classList.add("hidden");
    });
      
    document.getElementById("back-to-chats-btn").onclick = () => {
        document.getElementById("chat-section").style.display = "none"; // Скрываем чат
        document.getElementById("chats-section").style.display = "block"; // Показываем список чатов
        currentChatId = null; // Сбрасываем текущий chatId
    }; 

    function openChat(chatId, chatName) {
        document.getElementById("chats-section").style.display = "none";
        document.getElementById("chat-section").style.display = "block";
        document.getElementById("chat-name").textContent = chatName;
        currentChatId = chatId;
    
        // Устанавливаем обработчики для контекстного меню
        setupContextMenu();
    }

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
    try {
        const response = await fetch(`${BASE_URL}/messages/history/${chatId}`);
        if (!response.ok) {
            const error = await response.json();
            console.error("Ошибка от API:", error);
            alert(`Ошибка: ${error.detail || "Не удалось загрузить историю сообщений"}`);
            return;
        }

        const data = await response.json();
        console.log("История сообщений:", data);

        const chatWindow = document.getElementById("chat-window");
        chatWindow.innerHTML = ""; // Очистка окна чата

        data.history.forEach(msg => {
            const msgDiv = document.createElement("div");

            // Основное сообщение
            const content = document.createElement("span");
            content.textContent = `${msg.timestamp} - ${msg.sender}: ${msg.content}`;
            msgDiv.appendChild(content);

            // Кнопка "Редактировать"
            const editBtn = document.createElement("button");
            editBtn.textContent = "Редактировать";
            editBtn.onclick = () => editMessage(msg.id, content);
            msgDiv.appendChild(editBtn);

            // Кнопка "Удалить"
            const deleteBtn = document.createElement("button");
            deleteBtn.textContent = "Удалить";
            deleteBtn.onclick = () => deleteMessage(msg.id, msgDiv);
            msgDiv.appendChild(deleteBtn);

            chatWindow.appendChild(msgDiv);
        });
    } catch (error) {
        console.error("Ошибка загрузки чата:", error);
        alert("Не удалось загрузить чат. Проверьте подключение к серверу.");
    }
    ws.onmessage = (event) => {
        console.log("Новое сообщение через WebSocket:", event.data);
        const chatWindow = document.getElementById("chat-window");
    
        // Создаём элемент для нового сообщения
        const messageDiv = document.createElement("div");
        messageDiv.textContent = event.data;
    
        // Добавляем сообщение в окно чата
        chatWindow.appendChild(messageDiv);
    
        // Прокручиваем окно чата вниз
        chatWindow.scrollTop = chatWindow.scrollHeight;
    };    
}

async function editMessage(messageId, contentElement) {
    const newContent = prompt("Введите новое сообщение:", contentElement.textContent.split(": ")[1]);
    if (!newContent) return;

    try {
        const response = await fetch(`${BASE_URL}/messages/edit/${messageId}`, {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ content: newContent }), // Формируем JSON-объект
        });

        if (response.ok) {
            alert("Сообщение обновлено!");
            contentElement.textContent = contentElement.textContent.split(": ")[0] + `: ${newContent} (ред.)`;
        } else {
            const error = await response.json();
            console.error("Ошибка от сервера:", error);
            alert(`Ошибка: ${JSON.stringify(error)}`);
        }
    } catch (err) {
        console.error("Ошибка сети:", err);
        alert("Ошибка сети. Проверьте подключение к серверу.");
    }
}

async function deleteMessage(messageId, messageElement) {
    const confirmDelete = confirm("Вы уверены, что хотите удалить это сообщение?");
    if (!confirmDelete) return;

    const response = await fetch(`${BASE_URL}/messages/delete/${messageId}`, { method: "DELETE" });

    if (response.ok) {
        alert("Сообщение удалено!");
        messageElement.remove(); // Удаляем сообщение из DOM
    } else {
        const error = await response.json();
        alert("Ошибка: " + error.detail);
    }
}
