# voice-server

Самодостаточный каталог: **HTTP-gateway на Go** (UI + API + WebSocket) и **Python voice-worker** (WhisperX, pyannote, SQLite). Не зависит от родительского репозитория: свой `go.mod`, свои пути.

| Компонент | Путь | Назначение |
|-----------|------|------------|
| Gateway | [`cmd/gateway`](cmd/gateway) | Точка входа: флаги, HTTP-сервер |
| Библиотека | [`internal/`](internal) | Слои приложения (см. [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)) |
| Веб-UI | [`web/static`](web/static) | Статика (HTML/CSS/JS) |
| Voice-worker | [`voice-worker`](voice-worker) | FastAPI + модели |

**[Быстрый старт](docs/QUICKSTART.md)** · **[colab.ipynb](colab.ipynb)** (запуск в Colab) · [установка](docs/SETUP.md) · [Colab](COLAB.md)

## Быстрый старт (локально)

1. Поднять Python voice-worker (GPU/Colab или машина с CUDA):

   ```bash
   cd voice-worker
   pip install -r requirements.txt
   export HF_TOKEN=...                 # для pyannote
   export VOICE_SERVER_API_KEY=barchik
   ./run.sh
   ```

2. В другом терминале — gateway (из **корня этого каталога** `services/voice-server`):

   ```bash
   go run ./cmd/gateway -voice-url=http://127.0.0.1:8000
   ```

3. Открыть в браузере: http://localhost:8080

Флаги gateway: `-voice-url` (обязательно), `-port` (по умолчанию 8080), `-api-key`, `-static` (по умолчанию ищется `web/static` или `static`).

## Сборка бинарника

```bash
go build -o bin/gateway ./cmd/gateway
```

## Связанные документы

- [docs/SETUP.md](docs/SETUP.md) — зависимости, переменные окружения, типичные ошибки
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — слои и границы модулей
- [COLAB.md](COLAB.md) — перенос на Google Drive / Colab
- **[colab.ipynb](colab.ipynb)** — ноутбук для запуска voice-worker в Google Colab
