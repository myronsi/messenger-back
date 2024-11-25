const BASE_URL = "http://127.0.0.1:8000";
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
        initChat(username);
    } else {
        document.getElementById("login-message").textContent = message.detail;
    }
};

// Инициализация чата
function initChat(username) {
    document.getElementById("login-section").style.display = "none";
    document.getElementById("register-section").style.display = "none";
    document.getElementById("chat-section").style.display = "block";

    ws = new WebSocket(`ws://127.0.0.1:8000/ws/${username}`);
    ws.onmessage = (event) => {
        const chatWindow = document.getElementById("chat-window");
        const messageDiv = document.createElement("div");
        messageDiv.textContent = event.data;
        chatWindow.appendChild(messageDiv);
        chatWindow.scrollTop = chatWindow.scrollHeight;
    };

    document.getElementById("send-btn").onclick = () => {
        const messageInput = document.getElementById("message-input");
        ws.send(messageInput.value);
        messageInput.value = "";
    };
}
