# Запуск voice-worker в Google Colab

База голосов — SQLite. И **код**, и **база** хранятся на **Google Drive** — так ничего не теряется между сессиями Colab.

На Colab нужен **только** каталог **`voice-worker`** (Python). Gateway на Go и папка `web/` на Drive не нужны.

Структура на диске (пример):

```
MyDrive/
└── mafia-voice/
    ├── voice-worker/              ← достаточно скопировать только это (app/, requirements.txt, …)
    └── voice_registry.sqlite      ← опционально; или задаётся VOICE_SERVER_DB
```

## 1. Подготовка (один раз)

Скопируйте **`services/voice-server/voice-worker`** в Google Drive, например в `mafia-voice/voice-worker`.  
Весь репозиторий или каталог `voice-server` целиком копировать не обязательно.

Сделать это можно любым способом:
- загрузить zip через браузер на drive.google.com и распаковать через Colab;
- или `git clone` в сессии Colab и скопировать в Drive (`cp -r ...`).

После этого **повторять не нужно** — код и база живут на Drive постоянно.

## 2. Порядок действий при каждом запуске

1. **Сначала** смонтируйте Google Drive (ячейка с `drive.mount`).
2. **Затем** задайте переменные окружения и **запустите сервер**.

Всё — код уже на Drive, никакого клонирования или распаковки.

## 3. Переменные окружения

```python
import os

VOICE_WORKER_ROOT = "/content/drive/MyDrive/mafia-voice/voice-worker"

DB_DIR = "/content/drive/MyDrive/mafia-voice"
os.environ["VOICE_SERVER_DB"] = f"{DB_DIR}/voice_registry.sqlite"
os.environ["VOICE_SERVER_DEVICE"] = "cuda"
os.environ["VOICE_SERVER_API_KEY"] = "barchik"
```

## 4. Зависимости и секреты

```bash
pip install -r /content/drive/MyDrive/mafia-voice/voice-worker/requirements.txt
```

Нужны:

- `HF_TOKEN` — токен Hugging Face (модели pyannote / gated);
- `VOICE_SERVER_API_KEY` — тот же ключ, что в Go gateway (`-api-key`, по умолчанию `barchik`).

В Colab удобно положить токены в **секреты** (значок ключа слева) и читать через `userdata.get`.

> `pip install` нужно выполнять при каждой новой сессии — это ограничение Colab.

## 5. GPU

Убедитесь, что среда: **Runtime → Change runtime type → GPU**.

```python
os.environ["VOICE_SERVER_DEVICE"] = "cuda"
```

## 6. Запуск сервера

Рабочая директория — **`voice-worker`** (там лежит пакет `app`):

```bash
cd /content/drive/MyDrive/mafia-voice/voice-worker
export PYTHONPATH="/content/drive/MyDrive/mafia-voice/voice-worker"

uvicorn app.main:app --host 0.0.0.0 --port 8000
```

Или в фоне:

```bash
nohup uvicorn app.main:app --host 0.0.0.0 --port 8000 > /tmp/voice-worker.log 2>&1 &
```

## 7. Доступ с вашего ПК (Go gateway)

Colab не даёт публичный URL сам по себе. Варианты:

- **ngrok**: установить `pyngrok`, выдать токен, `ngrok.connect(8000)`, в gateway указать выданный `https://....ngrok-free.app` как `-voice-url`.
- **cloudflared** / другой туннель по желанию.

## 8. Готовый ноутбук

В корне `voice-server` лежит **[colab.ipynb](colab.ipynb)**. Откройте его в [Google Colab](https://colab.research.google.com/) через **Файл → Загрузить блокнот** (или загрузите из репозитория на Drive и откройте оттуда). Последовательность ячеек: Drive → переменные → `pip` → ngrok + uvicorn. При необходимости поправьте путь `VOICE_WORKER_ROOT` под вашу папку на Drive.

## 9. Что сохраняется между сессиями

| Что | Сохраняется? |
|-----|-------------|
| `/content/` (временные файлы) | Нет |
| Google Drive (код + БД) | Да |
| Установленные пакеты (`pip install`) | Нет — нужно каждый раз |

## 10. Где физически лежит БД

| Среда | Файл |
|--------|------|
| Локально без переменных | внутри `voice-worker/data/voice_registry.sqlite` |
| Colab + Drive | `/content/drive/MyDrive/mafia-voice/voice_registry.sqlite` (при `VOICE_SERVER_DB` как выше) |

Gateway **не хранит** базу; он только ходит по HTTP на URL voice-worker.
