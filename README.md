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

```json
{
  "whisper": {
    "binary": "./whisper-cpp/main",     // путь к бинарнику
    "model":  "./models/ggml-medium.bin",
    "language": "ru"
  },
  "ollama": {
    "model": "llama3.1",               // модель которая у тебя установлена
    "temperature": 0.3
  },
  "analysis": {
    "buffer_lines": 3,                 // реплик перед отправкой в ollama
    "context_window": 20               // реплик контекста
  }
}
```

Промпты редактируются там же в `prompts.system` — они уже настроены на JSON-ответ.

### 3. Сборка

```bash
go build -o mafia-analyzer ./cmd/mafia-analyzer/
```

### 4. Запуск с аудио файлом

```bash
# Прямой режим (проще для теста):
./mafia-analyzer -audio game_recording.wav

# С кастомным конфигом:
./mafia-analyzer -audio game_recording.wav -config config/config.json
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
│   └── config.json                 # настройки + промпты
└── test-with-vlc.sh                # пайп через VLC для симуляции стрима
```

## Следующий этап (Phase 2)

- WebSocket hub в Go для push-уведомлений фронтенду
- Node.js / React дашборд с живым графиком подозрений по игрокам
- Идентификация игроков по голосу (speaker diarization)
- Экспорт полного лога игры в JSON
