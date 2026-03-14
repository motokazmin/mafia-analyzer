# Mafia Analyzer — Phase 1

Сервис транскрибирует аудио игры через whisper.cpp и анализирует поведение игроков через Ollama.

```
Audio File → whisper.cpp (subprocess) → transcript lines → Go service → Ollama → stdout analysis
```

## Быстрый старт

### 1. Зависимости

```bash
# whisper.cpp
git clone https://github.com/ggerganov/whisper.cpp
cd whisper.cpp && make
bash ./models/download-ggml-model.sh medium  # для русского языка

# Ollama
curl -fsSL https://ollama.ai/install.sh | sh
ollama pull llama3.1   # или любая другая модель
```

### 2. Настройка

Отредактируй `config/config.json`:

#### Локальная Ollama (по умолчанию)

```json
{
  "whisper": {
    "binary": "./whisper-cpp/main",
    "model":  "./models/ggml-medium.bin",
    "language": "ru"
  },
  "ollama": {
    "base_url": "http://localhost:11434",
    "model": "llama3.1",
    "temperature": 0.3,
    "stream": true,
    "api_key": "",
    "headers": {}
  },
  "analysis": {
    "buffer_lines": 3,
    "context_window": 20
  }
}
```

#### Облачная Ollama (Google Colab / ngrok)

Для работы с облачной Ollama через Google Colab или ngrok туннель:

1. Настройте Ollama на удаленном сервере (например, в Google Colab)
2. Создайте туннель (ngrok или другой сервис)
3. Обновите конфиг:

```json
{
  "ollama": {
    "base_url": "https://your-ngrok-url.ngrok.io",
    "model": "llama3.2:latest",
    "temperature": 0.2,
    "stream": true,
    "api_key": "your-api-key-if-needed",
    "headers": {
      "X-Custom-Header": "value-if-needed"
    }
  }
}
```

**Параметры:**
- `base_url` — URL облачного сервера Ollama (например, через ngrok)
- `api_key` — API ключ для аутентификации (если требуется)
- `headers` — дополнительные HTTP заголовки (опционально)

Пример конфигурации для облачной Ollama: `config/config.cloud.example.json`

**Развертывание Ollama в Google Colab:**

Для автоматической настройки Ollama в Google Colab используйте скрипт:
```bash
# Скопируйте scripts/setup_ollama_colab.py в Google Colab
# Следуйте инструкциям в scripts/README_COLAB.md
```

#### Облачный Whisper (Google Colab)

Для работы с облачным Whisper через Google Colab:

1. Запустите скрипт `scripts/setup_whisper_colab.py` в Colab
2. Скопируйте публичный ngrok URL
3. Обновите конфиг:

```json
{
  "whisper": {
    "mode": "remote",
    "remote_url": "https://your-ngrok-url.ngrok-free.dev/whisper/file",
    "language": "ru",
    "model": "ggml-small"
  }
}
```

#### Захват микрофона

Для работы с микрофоном в реальном времени:

1. Убедитесь, что `ffmpeg` установлен: `sudo apt install ffmpeg` (Linux)
2. Настройте облачный Whisper (см. выше)
3. Обновите конфиг:

```json
{
  "whisper": {
    "mode": "remote",
    "remote_url": "https://your-ngrok-url.ngrok-free.dev/whisper/file",
    "microphone": {
      "device": "default",
      "sample_rate": 16000,
      "channels": 1,
      "chunk_sec": 5,
      "format": "wav",
      "ffmpeg_path": "ffmpeg"
    }
  }
}
```

4. Запустите с флагом `-mic`:

```bash
./mafia-analyzer -mic -config config/config.json
```

**Примечания:**
- На Linux используйте `device: "default"` (PulseAudio)
- На macOS используйте `device: ":0"` (avfoundation)
- На Windows используйте `device: "audio=Microphone"` (dshow)

Промпты редактируются там же в `prompts.system` — они уже настроены на JSON-ответ.

### 3. Сборка

```bash
go build -o mafia-analyzer ./cmd/mafia-analyzer/
```

### 4. Запуск

#### С аудио файлом

```bash
# Прямой режим (проще для теста):
./mafia-analyzer -audio game_recording.wav

# С кастомным конфигом:
./mafia-analyzer -audio game_recording.wav -config config/config.json
```

#### С микрофоном (требует облачный Whisper)

```bash
./mafia-analyzer -mic -config config/config.microphone.example.json
```

### 5. Тест через VLC (симуляция стрима)

```bash
chmod +x test-with-vlc.sh
./test-with-vlc.sh game_recording.mp3
```

## Что выводится в stdout

```
15:04:05 [CONFIG    ] model=llama3.1 | buffer=3 lines | context=20 lines
15:04:05 [START     ] launching whisper on: game.wav
15:04:07 [TRANSCRIPT] [1] Я думаю что Андрей ведёт себя подозрительно
15:04:07 [TRANSCRIPT] [2] Нет, он мирный, я за него ручаюсь
15:04:07 [TRANSCRIPT] [3] Давайте голосуем за Марину

────────────────────── АНАЛИЗ (2.3s) ──────────────────────
🎭 Подозрения:
  Андрей       [████░░░░░░] 4/10  Упоминается как подозрительный
  Нет (игрок)  [██████░░░░] 6/10  Активно защищает другого игрока
🔑 Ключевые фразы: я за него ручаюсь | давайте голосуем
📝 Вывод: Возможна связь между игроком 2 и Андреем. Требует наблюдения.
────────────────────────────────────────────────────────────
```

## Структура проекта

```
mafia-analyzer/
├── cmd/mafia-analyzer/main.go      # точка входа, цветной stdout лог
├── internal/
│   ├── whisper/runner.go           # запуск whisper как subprocess
│   ├── ollama/client.go            # HTTP клиент ollama + парсинг JSON
│   └── analyzer/analyzer.go       # буфер реплик + форматирование
├── config/
│   ├── config.go                   # загрузка конфига
│   ├── config.json                 # настройки + промпты
│   └── config.cloud.example.json   # пример облачной конфигурации
├── scripts/
│   ├── setup_ollama_colab.py       # скрипт развертывания Ollama в Colab
│   ├── setup_whisper_colab.py      # скрипт развертывания Whisper в Colab
│   ├── diagnose_colab.py           # диагностика проблем с Ollama/ngrok
│   └── README_COLAB.md             # инструкции по использованию Colab
└── test-with-vlc.sh                # пайп через VLC для симуляции стрима
```

## Следующий этап (Phase 2)

- WebSocket hub в Go для push-уведомлений фронтенду
- Node.js / React дашборд с живым графиком подозрений по игрокам
- Идентификация игроков по голосу (speaker diarization)
- Экспорт полного лога игры в JSON
