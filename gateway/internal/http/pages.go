package http

import (
	"net/http"
)

// HandleRoot перенаправляет на панель или страницу входа.
func (h *Handler) HandleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if _, err := h.userFromRequest(r); err == nil {
		http.Redirect(w, r, "/panel", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/login", http.StatusFound)
}

// HandleLoginPage отдаёт страницу входа; авторизованных перенаправляет на панель.
func (h *Handler) HandleLoginPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, err := h.userFromRequest(r); err == nil {
		http.Redirect(w, r, "/panel", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(loginPageHTML))
}

// HandlePanelPage отдаёт панель управления только авторизованным пользователям.
func (h *Handler) HandlePanelPage(panelHTML []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if _, err := h.userFromRequest(r); err != nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(panelHTML)
	}
}

// HandleLogout завершает сессию и возвращает на страницу входа.
func (h *Handler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_token",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/login", http.StatusFound)
}

const loginPageHTML = `<!DOCTYPE html>
<html lang="ru">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Вход — Streaming Processor</title>
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body {
            font-family: 'JetBrains Mono', 'Fira Code', monospace;
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
            background: linear-gradient(135deg, #1a1a2e 0%, #16213e 50%, #0f3460 100%);
            color: #e0e0e0;
            padding: 1.5rem;
        }
        .card {
            width: 100%;
            max-width: 420px;
            background: rgba(255,255,255,0.06);
            border: 1px solid rgba(255,255,255,0.12);
            border-radius: 16px;
            padding: 2rem;
            backdrop-filter: blur(12px);
        }
        h1 {
            font-size: 1.5rem;
            margin-bottom: 0.5rem;
            background: linear-gradient(90deg, #00d4ff, #7b2cbf);
            -webkit-background-clip: text;
            -webkit-text-fill-color: transparent;
        }
        .sub { color: #888; font-size: 0.85rem; margin-bottom: 1.5rem; line-height: 1.4; }
        label { display: block; font-size: 0.8rem; color: #94a3b8; margin: 0.75rem 0 0.35rem; }
        input {
            width: 100%;
            padding: 0.75rem 1rem;
            border-radius: 8px;
            border: 1px solid #334155;
            background: rgba(0,0,0,0.35);
            color: #e0e0e0;
            font-family: inherit;
        }
        .row { display: flex; gap: 0.5rem; margin-top: 1.25rem; }
        button {
            flex: 1;
            padding: 0.75rem;
            border: none;
            border-radius: 8px;
            font-family: inherit;
            font-weight: bold;
            cursor: pointer;
        }
        .primary { background: linear-gradient(90deg, #00d4ff, #7b2cbf); color: #fff; }
        .secondary { background: rgba(255,255,255,0.1); color: #e0e0e0; border: 1px solid #444; }
        #msg { margin-top: 1rem; font-size: 0.85rem; min-height: 1.2em; }
        .err { color: #f87171; }
        .ok { color: #4ade80; }
    </style>
</head>
<body>
    <div class="card">
        <h1>Streaming Processor</h1>
        <p class="sub">Войдите или зарегистрируйтесь, чтобы открыть панель управления стендом.</p>
        <label for="username">Логин</label>
        <input id="username" type="text" autocomplete="username" placeholder="login" value="student">
        <label for="password">Пароль</label>
        <input id="password" type="password" autocomplete="current-password" placeholder="password" value="student">
        <div class="row">
            <button type="button" class="primary" onclick="doLogin()">Вход</button>
            <button type="button" class="secondary" onclick="doRegister()">Регистрация</button>
        </div>
        <p id="msg"></p>
    </div>
    <script>
        function credentials() {
            return {
                username: document.getElementById('username').value.trim(),
                password: document.getElementById('password').value
            };
        }
        function setMsg(text, ok) {
            const el = document.getElementById('msg');
            el.textContent = text;
            el.className = ok ? 'ok' : 'err';
        }
        async function authRequest(path) {
            const resp = await fetch(path, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                credentials: 'same-origin',
                body: JSON.stringify(credentials())
            });
            const text = await resp.text();
            let data;
            try { data = JSON.parse(text); } catch { data = { error: text }; }
            if (!resp.ok) throw new Error(data.error || text || resp.statusText);
            return data;
        }
        async function doLogin() {
            try {
                setMsg('Вход…', true);
                const data = await authRequest('/api/login');
                localStorage.setItem('token', data.token);
                setMsg('Успешно. Переход на панель…', true);
                window.location.href = '/panel';
            } catch (e) { setMsg(e.message, false); }
        }
        async function doRegister() {
            try {
                setMsg('Регистрация…', true);
                const data = await authRequest('/api/register');
                localStorage.setItem('token', data.token);
                setMsg('Аккаунт создан. Переход на панель…', true);
                window.location.href = '/panel';
            } catch (e) { setMsg(e.message, false); }
        }
        document.getElementById('password').addEventListener('keydown', function(e) {
            if (e.key === 'Enter') doLogin();
        });
    </script>
</body>
</html>`
