# Как начать: Colab + локальный компьютер

Ниже — минимум шагов: Python в Google Colab, Go у вас на машине, браузер для интерфейса.

---

## 1. Python (voice-worker) в Colab

На Colab нужен **только Python-сервис** — каталог **`voice-worker/`** (`app/`, `requirements.txt`, `run.sh`).  
**Не обязательно** копировать весь `voice-server`: Go-gateway и веб-статика запускаются **только на вашем компьютере**.

1. Скопируйте на **Google Drive** один каталог **`voice-worker`** из репозитория (`services/voice-server/voice-worker`), например:  
   `MyDrive/mafia-voice/voice-worker`

2. В Colab: **Runtime → Change runtime type → GPU**.

3. Смонтируйте Drive (ячейка):
   ```python
   from google.colab import drive
   drive.mount('/content/drive')
   ```

4. Задайте пути и переменные (подставьте свой путь к `voice-worker` на Drive):
   ```python
   import os
   ROOT = "/content/drive/MyDrive/mafia-voice/voice-worker"
   os.chdir(ROOT)
   os.environ["PYTHONPATH"] = ROOT
   os.environ["VOICE_SERVER_DEVICE"] = "cuda"
   os.environ["VOICE_SERVER_API_KEY"] = "barchik"
   os.environ["VOICE_SERVER_DB"] = "/content/drive/MyDrive/mafia-voice/voice_registry.sqlite"
   os.environ["HF_TOKEN"] = "ваш_токен_HuggingFace"  # для pyannote
   ```

5. Установите зависимости (каждая новая сессия Colab):
   ```bash
   !pip install -q -r requirements.txt
   ```

6. Сделайте сервер доступным с интернета: **ngrok** (или другой туннель). Пример с ngrok:
   - Зарегистрируйтесь на ngrok, возьмите токен.
   - В Colab: `pip install pyngrok`, затем в коде вызовите `ngrok.connect(8000)` и получите URL вида `https://xxxx.ngrok-free.app`.

7. Запустите API:
   ```bash
   !uvicorn app.main:app --host 0.0.0.0 --port 8000
   ```
   Либо запускайте uvicorn в фоне и в отдельной ячейке вызывайте ngrok.

8. **Скопируйте публичный HTTPS-URL** (например `https://....ngrok-free.app`) — он понадобится Go-клиенту как адрес voice-worker.

Подробности: [COLAB.md](../COLAB.md). **Готовый ноутбук для Colab:** [colab.ipynb](../colab.ipynb) (в корне `voice-server` — загрузите в Colab через «Файл → Загрузить блокнот»).

---

## 2. Go (gateway) на локальном компьютере

1. Установите **Go** (1.24+).

2. Склонируйте репозиторий или скопируйте каталог **`services/voice-server`** на машину.

3. В терминале:
   ```bash
   cd путь/к/services/voice-server
   go run ./cmd/gateway -voice-url=https://ВАШ_URL_ИЗ_NGROK
   ```
   Подставьте **тот же** URL, что выдал ngrok в Colab (без слэша в конце).

4. По умолчанию интерфейс откроется на **http://localhost:8080**  
   Другой порт: `-port 9090`.

5. Ключ API по умолчанию совпадает с Colab: `-api-key barchik`. Если в Colab задали другой `VOICE_SERVER_API_KEY`, укажите его же в `-api-key`.

6. **Текст партий на диске (для последующего анализа):** по умолчанию gateway пишет SQLite **`data/game_log.sqlite`** рядом с рабочей директорией. Там сохраняются реплики с пометкой источника (**файл** vs **микрофон**) и режимом (**ingest** / **file** / **record**). Отключить запись:  
   `go run ./cmd/gateway -voice-url=... -game-db ""`  
   Выгрузка партий: `GET http://localhost:8080/api/games/sessions` и `.../sessions/{id}/segments` (подробнее в [SETUP.md](SETUP.md)). В ответах API и в статусе сессии может быть поле **`game_session_id`**.

---

## 3. Как пользоваться фронтом

1. Откройте в браузере: **http://localhost:8080**

2. **Обучить на записи** — загрузите длинный файл целиком (ingest): сервер строит диаризацию по полному файлу, справа появятся карточки спикеров; можно назначить имена по тексту.

3. **Тест на файле** — тот же или другой файл: обработка **чанками**, как «живая» игра; лог в центре заполняется по мере обработки.

4. **Живая игра** — запись с микрофона (нужен Linux с ALSA и ffmpeg; на Windows/Mac может не заработать без доработки).

5. Поле **«Спикеров»** — необязательно; если указать число, передаётся как подсказка для диаризации.

6. **Стоп** — остановить текущий режим (file/record). Индикатор сверху показывает ожидание / обработку / запись.

7. **Серые строки** в логе — спикер без имени; клик по строке (если есть `voice_id`) открывает выбор имени, либо введите новое и **OK**.

8. **Сохранить лог** — скачивает текстовый файл с репликами.

9. Если страница «не грузится», проверьте: в Colab запущен uvicorn, ngrok указывает на порт 8000, в Go-команде верный `-voice-url`.

---

Итого: **Colab** = Python + GPU + ngrok; **локально** = `go run ./cmd/gateway -voice-url=...`; **браузер** = `localhost:8080`; **архив партий** = `data/game_log.sqlite` + `/api/games/...`.
