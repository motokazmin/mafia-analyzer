# voice-worker

Python-сервис (FastAPI): WhisperX, pyannote, WavLM, реестр голосов в SQLite.

Запуск и переменные окружения — в [../docs/SETUP.md](../docs/SETUP.md). Colab — в [../COLAB.md](../COLAB.md). Общая архитектура gateway + worker — в [../docs/ARCHITECTURE.md](../docs/ARCHITECTURE.md).

## Запуск

```bash
./run.sh
```

По умолчанию: `0.0.0.0:8000`, ключ API — `VOICE_SERVER_API_KEY` (синхронизируйте с gateway `-api-key`).

## Полезные эндпоинты (с заголовком `X-API-Key`)

| Метод | Путь | Назначение |
|-------|------|------------|
| GET | `/health` | Проверка |
| POST | `/process_chunk` | Основной аудио-пайплайн |
| POST | `/reset` | Сброс сессии и выгрузка CUDA (профили в SQLite не удаляет) |
| GET | `/voices` | Список голосов |
| PATCH | `/voices/{id}` | Переименование |
| PATCH | `/voices/{id}/flags` | Флаг «ненадёжный» |
| POST | `/voices/merge` | `{ "source_id", "target_id" }` |
| POST | `/voices/wipe` | Полная очистка таблицы голосов |

Пороги сходства: **`VOICE_THRESHOLD_PRESET`** (`balanced` / `strict` / `loose`) и переменные `THRESHOLD_*` — см. `app/config.py`.
