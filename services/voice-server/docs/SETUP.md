# Установка и использование

Все команды ниже выполняются из каталога **`services/voice-server`** (корень модуля `voice-server`), если не указано иначе.

## Требования

- **Go** 1.24+
- **Python** 3.10+ (для voice-worker)
- **ffmpeg** в `PATH` (на gateway — для режимов file/record)
- Для записи с микрофона (Linux): ALSA, устройство `default`
- Для Python-моделей: CUDA (рекомендуется), `HF_TOKEN` для pyannote

## 1. Voice-worker (Python)

```bash
cd voice-worker
python -m venv .venv
source .venv/bin/activate   # Windows: .venv\Scripts\activate
pip install -r requirements.txt

export HF_TOKEN=hf_...                    # Hugging Face
export VOICE_SERVER_API_KEY=barchik       # тот же ключ, что у gateway -api-key
# опционально:
# export VOICE_SERVER_DB=/abs/path/voice_registry.sqlite
# export VOICE_SERVER_DEVICE=cuda

./run.sh
```

По умолчанию слушает `0.0.0.0:8000`. Проверка: `GET http://127.0.0.1:8000/health` (если эндпоинт есть в `app/main.py`).

## 2. Gateway (Go)

Из **`services/voice-server`**:

```bash
go run ./cmd/gateway -voice-url=http://127.0.0.1:8000
```

Параметры:

| Флаг | По умолчанию | Описание |
|------|----------------|----------|
| `-voice-url` | (обязателен) | Базовый URL voice-worker без завершающего `/` |
| `-port` | 8080 | Порт HTTP gateway |
| `-api-key` | barchik | Заголовок `X-API-Key` к Python |
| `-static` | авто: `web/static` или `static` | Каталог статики UI |
| `-game-db` | `data/game_log.sqlite` | Файл SQLite с текстом партий для последующего анализа; пустая строка отключает запись |

### Локальная база партий (для анализа игры)

Gateway сохраняет реплики в SQLite рядом с бинарником (по умолчанию **`data/game_log.sqlite`**). У каждой партии:

- **`capture_source`**: `file` (любой загруженный/прогнанный файл) или `microphone` (живая запись);
- **`session_mode`**: `ingest` | `file` | `record` — уточняет сценарий;
- **`source_filename`**: имя файла, если известно;
- таблица **`game_segments`**: спикер, текст, тайминги, `voice_id`, `match_score` (уверенность по эмбеддингу), порядок `seq`;
- таблица **`game_segment_overrides`** — ручное переназначение спикера для реплики (см. UI, кнопка ✎ у строки).

HTTP (для выгрузки / своих скриптов анализа):

- `GET /api/games/sessions?limit=100` — список партий;
- `GET /api/games/sessions/{id}` — метаданные партии;
- `GET /api/games/sessions/{id}/segments` — все реплики по порядку (учтены переопределения);
- `POST` / `DELETE` `.../sessions/{id}/segments/{seq}/override` — ручное назначение / сброс.

В ответах `POST /api/ingest`, `POST /api/session/start` и в `GET /api/session/status` при активной партии может быть поле **`game_session_id`**, в статусе — **`source_filename`** (имя файла для ingest/file).

**Полный сброс данных** (журнал партий на gateway + все голоса в Python): `POST /api/data/reset` с телом `{"confirm": true}` (сессия должна быть остановлена) или кнопка «Очистить базы» в UI. Только реестр голосов на Python: `POST /voices/wipe` с заголовком `X-API-Key`.

Сборка:

```bash
go build -o bin/gateway ./cmd/gateway
./bin/gateway -voice-url=http://127.0.0.1:8000
```

## 3. Сценарий с Colab / ngrok

1. Запустите voice-worker в Colab (см. [COLAB.md](../COLAB.md)).
2. Пробросьте URL (ngrok): `https://xxxx.ngrok-free.app`.
3. На локальной машине:  
   `go run ./cmd/gateway -voice-url=https://xxxx.ngrok-free.app`

## 4. Базы данных (две независимые SQLite)

| Роль | Файл по умолчанию | Переопределение |
|------|-------------------|-----------------|
| **Партии и реплики** (gateway) | `data/game_log.sqlite` рядом с корнем модуля `voice-server` | Флаг **`-game-db`** |
| **Реестр голосов** (voice-worker) | `voice-worker/data/voice_registry.sqlite` | **`VOICE_SERVER_DB`** |

См. также [ARCHITECTURE.md](ARCHITECTURE.md).

### Voice-worker: пороги и пресеты

В окружении Python (не в Go): **`VOICE_THRESHOLD_PRESET`** — `balanced` (по умолчанию), **`strict`** (меньше слияний разных людей, больше новых ID), **`loose`**. Отдельные **`THRESHOLD_*`** в env переопределяют числа.

### Прочие API voice-worker (через gateway)

- `POST /api/speakers/merge` — объединить два профиля;
- `PATCH /api/speakers/{id}/flags` — пометить «ненадёжный» кластер.

Полный список: [ARCHITECTURE.md](ARCHITECTURE.md).

## 5. Независимость от монорепозитория

Модуль `voice-server` имеет свой **`go.mod`**. Родительский `mafia-analyzer` на него **не ссылается** через `replace`. Достаточно скопировать каталог `services/voice-server` в любое место и выполнять `go`/`pip` из него.
