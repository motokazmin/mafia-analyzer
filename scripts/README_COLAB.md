# Развертывание Ollama в Google Colab

Этот скрипт автоматизирует развертывание Ollama в Google Colab для использования с mafia-analyzer.

## Быстрый старт

### 1. Откройте Google Colab

Создайте новый ноутбук: https://colab.research.google.com/

### 2. Загрузите скрипт

Скопируйте содержимое `scripts/setup_ollama_colab.py` в ячейку Colab или загрузите файл.

### 3. Настройте параметры

Отредактируйте в начале скрипта:

```python
# Модели для загрузки
REQUIRED_MODELS = ["qwen2.5:14b"]  # или другие модели

# Токен ngrok (опционально, для бесплатного доступа)
NGROK_AUTH_TOKEN = "your-ngrok-token-here"
```

### 4. Получите ngrok токен (опционально)

Для доступа к Ollama извне Colab:

1. Зарегистрируйтесь на https://dashboard.ngrok.com/signup
2. Скопируйте токен из https://dashboard.ngrok.com/get-started/your-authtoken
3. Вставьте токен в `NGROK_AUTH_TOKEN`

**Без ngrok**: Ollama будет доступна только внутри Colab (для тестирования).

### 5. Запустите скрипт

Выполните ячейку в Colab. Скрипт выполнит:

- ✅ Монтирование Google Drive
- ✅ Установку Ollama
- ✅ Загрузку моделей
- ✅ Настройку ngrok туннеля (если указан токен)

### 6. Обновите конфиг

После запуска скрипт выведет публичный URL ngrok. Обновите `config/config.json`:

```json
{
  "ollama": {
    "base_url": "https://your-ngrok-url.ngrok.io",
    "model": "qwen2.5:14b",
    "temperature": 0.2,
    "stream": true,
    "api_key": "",
    "headers": {}
  }
}
```

## Использование

### Запуск mafia-analyzer

```bash
./mafia-analyzer -audio game.wav -config config/config.json
```

### Проверка подключения

Скрипт автоматически проверяет подключение к Ollama. Вы также можете проверить вручную:

```bash
curl http://localhost:11434/api/tags  # внутри Colab
curl https://your-ngrok-url.ngrok.io/api/tags  # извне
```

## Важные замечания

### ⚠️ Сессия Colab

- **Сессия должна оставаться активной** - после перезапуска Colab нужно запустить скрипт снова
- Бесплатные сессии Colab имеют ограничения по времени (около 12 часов)
- Для постоянной работы рассмотрите Colab Pro

### 💾 Модели на Google Drive

Модели сохраняются в `/content/drive/MyDrive/mafia/ollama_models`, что позволяет:
- Не загружать модели при каждом перезапуске
- Использовать один диск для нескольких сессий

### 🔒 Безопасность

- Ngrok туннель публичный - любой с URL может использовать ваш Ollama
- Для продакшена используйте:
  - Ngrok с аутентификацией
  - VPN туннель
  - Приватный сервер

## Устранение проблем

### Диагностика проблем

Если вы получаете ошибку `ERR_NGROK_3200: The endpoint is offline`, используйте диагностический скрипт:

```python
# Скопируйте содержимое scripts/diagnose_colab.py в ячейку Colab
# Или выполните:
!python scripts/diagnose_colab.py
```

Скрипт проверит:
- ✅ Запущен ли процесс Ollama
- ✅ Отвечает ли Ollama API локально
- ✅ Запущен ли процесс ngrok
- ✅ Доступен ли Ollama через ngrok URL
- ✅ Слушает ли что-то на порту 11434

### Ollama не запускается

```python
# Проверьте статус
!ps aux | grep ollama
!ollama list

# Если не запущен, запустите вручную
!pkill ollama  # Убить старые процессы
!ollama serve &  # Запустить в фоне
```

### Ngrok не работает / Endpoint offline

Если вы получаете ошибку `ERR_NGROK_3200: The endpoint is offline`:

1. **Проверьте, что Ollama запущен:**
   ```python
   !ps aux | grep ollama
   !curl http://localhost:11434/api/tags
   ```

2. **Если Ollama не запущен, запустите вручную:**
   ```python
   !pkill ollama  # Убить старые процессы
   !ollama serve &  # Запустить в фоне
   ```

3. **Подождите 10-20 секунд** после запуска Ollama и ngrok

4. **Проверьте статус ngrok:**
   - Откройте http://localhost:4040 в Colab (через ngrok web interface)
   - Убедитесь, что туннель активен и показывает статус "online"

5. **Проверьте доступность через ngrok URL:**
   ```python
   !curl https://your-ngrok-url.ngrok.io/api/tags
   ```

6. **Если все еще не работает:**
   - Перезапустите ngrok: `!pkill ngrok && ngrok http 11434 &`
   - Убедитесь, что порт 11434 не занят другим процессом
   - Проверьте, что Ollama действительно слушает на порту 11434: `!netstat -tlnp | grep 11434`

### Модель не загружается

- Проверьте свободное место на диске
- Убедитесь, что модель существует: `ollama list`
- Попробуйте загрузить вручную: `ollama pull qwen2.5:14b`

## Альтернативные варианты

### Использование без ngrok

Если не нужен внешний доступ, просто не указывайте `NGROK_AUTH_TOKEN`. Ollama будет работать только внутри Colab.

### Использование других туннелей

Вместо ngrok можно использовать:
- **Cloudflare Tunnel** (бесплатно)
- **localtunnel** (бесплатно, но менее стабильно)
- **serveo** (бесплатно)

## Следующие шаги

После настройки Ollama в облаке, следующий этап - настройка облачного Whisper (будет добавлено позже).
