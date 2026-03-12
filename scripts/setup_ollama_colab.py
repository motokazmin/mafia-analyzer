#!/usr/bin/env python3
"""
Скрипт для развертывания Ollama в Google Colab
Устанавливает Ollama, загружает модели и настраивает ngrok туннель
"""

import os
import subprocess
import time
import sys
from pathlib import Path

# Конфигурация
OLLAMA_MODELS_DIR = '/content/drive/MyDrive/mafia/ollama_models'
REQUIRED_MODELS = ["qwen2.5:14b"]  # Модели для загрузки
OLLAMA_PORT = 11434
NGROK_AUTH_TOKEN = '3AoDR90lG7GchjAwnPxAIdAOisx_5HFZ54HKyxfYTHcuNqMYa'  # Ваш токен ngrok

def print_step(step_num, message):
    """Красивый вывод шагов"""
    print(f"\n{'='*60}")
    print(f"ШАГ {step_num}: {message}")
    print(f"{'='*60}\n")

def run_command(cmd, check=True, shell=False):
    """Выполняет команду и обрабатывает ошибки"""
    try:
        if isinstance(cmd, str) and not shell:
            cmd = cmd.split()
        result = subprocess.run(
            cmd,
            shell=shell,
            check=check,
            capture_output=True,
            text=True
        )
        return result
    except subprocess.CalledProcessError as e:
        print(f"❌ Ошибка выполнения команды: {e}")
        if e.stdout:
            print(f"STDOUT: {e.stdout}")
        if e.stderr:
            print(f"STDERR: {e.stderr}")
        raise

def check_colab():
    """Проверяет, что скрипт запущен в Google Colab"""
    try:
        from google.colab import drive
        return True
    except ImportError:
        print("⚠️  Внимание: Скрипт предназначен для Google Colab")
        print("   Некоторые функции могут не работать вне Colab")
        return False

def mount_drive():
    """Монтирует Google Drive"""
    print_step(1, "Монтирование Google Drive")
    try:
        from google.colab import drive
        drive.mount('/content/drive', force_remount=False)
        print("✅ Google Drive успешно смонтирован")
        return True
    except Exception as e:
        print(f"❌ Ошибка монтирования Drive: {e}")
        return False

def setup_ollama_environment():
    """Настраивает переменные окружения для Ollama"""
    print_step(2, "Настройка окружения Ollama")

    os.environ['OLLAMA_MODELS'] = OLLAMA_MODELS_DIR
    # ИСПРАВЛЕНИЕ: разрешаем Ollama принимать внешние подключения
    os.environ['OLLAMA_HOST'] = '0.0.0.0'
    os.makedirs(OLLAMA_MODELS_DIR, exist_ok=True)

    print(f"✅ Директория для моделей: {OLLAMA_MODELS_DIR}")
    print(f"✅ OLLAMA_HOST установлен: 0.0.0.0 (разрешены внешние подключения)")
    return True

def install_ollama():
    """Устанавливает Ollama, если еще не установлена"""
    print_step(3, "Установка Ollama")

    if os.path.exists('/usr/local/bin/ollama'):
        print("✅ Ollama уже установлена")
        return True

    print("📦 Установка зависимостей...")
    try:
        run_command("apt-get update -qq", shell=True)
        run_command("apt-get install -y -qq zstd curl", shell=True)

        print("📦 Установка Ollama...")
        run_command("curl -fsSL https://ollama.com/install.sh | sh", shell=True)

        # Проверка установки
        result = run_command(["ollama", "--version"], check=False)
        if result.returncode == 0:
            print("✅ Ollama успешно установлена")
            print(f"   Версия: {result.stdout.strip()}")
            return True
        else:
            print("❌ Ollama установлена, но не работает")
            return False
    except Exception as e:
        print(f"❌ Ошибка установки Ollama: {e}")
        return False

def start_ollama_service():
    """Запускает Ollama сервис в фоне с поддержкой внешних подключений"""
    print_step(4, "Запуск Ollama сервиса")

    # Останавливаем существующий процесс если есть
    result = run_command(["pgrep", "-f", "ollama serve"], check=False)
    if result.returncode == 0:
        print("🔄 Перезапуск Ollama с правильными настройками...")
        run_command(["pkill", "-f", "ollama serve"], check=False)
        time.sleep(2)

    print("🚀 Запуск Ollama сервиса (OLLAMA_HOST=0.0.0.0)...")
    try:
        env = os.environ.copy()
        env['OLLAMA_HOST'] = '0.0.0.0'
        env['OLLAMA_MODELS'] = OLLAMA_MODELS_DIR

        subprocess.Popen(
            ["ollama", "serve"],
            env=env,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL
        )

        # Ждем запуска
        for i in range(15):
            time.sleep(1)
            try:
                result = run_command(
                    ["curl", "-s", f"http://localhost:{OLLAMA_PORT}/api/tags"],
                    check=False
                )
                if result.returncode == 0:
                    print(f"✅ Ollama сервис запущен на порту {OLLAMA_PORT}")
                    print(f"✅ Внешние подключения разрешены (0.0.0.0)")
                    return True
            except:
                pass
            print(f"   Ожидание запуска... ({i+1}/15)")

        print("⚠️  Ollama сервис запущен, но проверка не прошла")
        return True
    except Exception as e:
        print(f"❌ Ошибка запуска Ollama: {e}")
        return False

def check_model_installed(model_name):
    """Проверяет, установлена ли модель"""
    try:
        result = run_command(["ollama", "list"], check=False)
        if result.returncode == 0:
            return model_name in result.stdout
        return False
    except:
        return False

def download_models():
    """Загружает необходимые модели"""
    print_step(5, "Загрузка моделей Ollama")

    for model in REQUIRED_MODELS:
        print(f"\n📥 Проверка модели: {model}")

        if check_model_installed(model):
            print(f"✅ Модель {model} уже установлена")
        else:
            print(f"📥 Загрузка модели {model}...")
            print("   Это может занять несколько минут...")
            try:
                result = run_command(["ollama", "pull", model], check=True)
                print(f"✅ Модель {model} успешно загружена")
            except Exception as e:
                print(f"❌ Ошибка загрузки модели {model}: {e}")
                return False

    print("\n✅ Все модели готовы")
    return True

def install_ngrok():
    """Устанавливает ngrok"""
    print_step(6, "Установка ngrok")

    if os.path.exists('/usr/local/bin/ngrok'):
        print("✅ ngrok уже установлен")
        return True

    print("📦 Установка ngrok...")
    try:
        run_command(
            "curl -s https://ngrok-agent.s3.amazonaws.com/ngrok.asc | tee /etc/apt/trusted.gpg.d/ngrok.asc >/dev/null "
            "&& echo 'deb https://ngrok-agent.s3.amazonaws.com buster main' | tee /etc/apt/sources.list.d/ngrok.list "
            "&& apt update && apt install ngrok -y",
            shell=True
        )

        if os.path.exists('/usr/local/bin/ngrok'):
            print("✅ ngrok успешно установлен")
            return True
        else:
            print("❌ ngrok установлен, но не найден")
            return False
    except Exception as e:
        print(f"❌ Ошибка установки ngrok: {e}")
        return False

def setup_ngrok_tunnel():
    """Настраивает ngrok туннель для доступа к Ollama"""
    print_step(7, "Настройка ngrok туннеля")

    if not NGROK_AUTH_TOKEN:
        print("⚠️  NGROK_AUTH_TOKEN не установлен")
        print("   Для бесплатного использования ngrok:")
        print("   1. Зарегистрируйтесь на https://dashboard.ngrok.com/signup")
        print("   2. Скопируйте токен из https://dashboard.ngrok.com/get-started/your-authtoken")
        print("   3. Установите NGROK_AUTH_TOKEN в начале скрипта")
        print("\n   Продолжаю без ngrok...")
        return False

    try:
        # Останавливаем старый ngrok если есть
        run_command(["pkill", "-f", "ngrok"], check=False)
        time.sleep(1)

        # Авторизация ngrok
        print("🔐 Авторизация ngrok...")
        run_command(["ngrok", "config", "add-authtoken", NGROK_AUTH_TOKEN], check=True)

        # Запуск туннеля в фоне
        print("🚇 Запуск ngrok туннеля...")
        ngrok_process = subprocess.Popen(
            ["ngrok", "http", str(OLLAMA_PORT), "--log=stdout"],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL
        )

        # Ждем запуска
        time.sleep(4)

        # Получаем URL туннеля
        try:
            import urllib.request
            import json

            req = urllib.request.urlopen("http://localhost:4040/api/tunnels", timeout=5)
            data = json.loads(req.read().decode())
            tunnels = data.get("tunnels", [])

            if tunnels:
                public_url = tunnels[0].get("public_url", "")
                if public_url:
                    print(f"\n✅ Ngrok туннель создан!")
                    print(f"🌐 Публичный URL: {public_url}")
                    print(f"\n📋 Обновите config.json:")
                    print(f'   "base_url": "{public_url}"')
                    print(f"\n🔗 Проверка API:")
                    print(f"   {public_url}/api/tags")
                    print(f"\n⚠️  При первом открытии в браузере нажмите 'Visit Site'")
                    print(f"   Для API-запросов добавляйте заголовок:")
                    print(f'   ngrok-skip-browser-warning: true')
                    return True
            else:
                print("⚠️  Туннели не найдены, проверьте http://localhost:4040")
        except Exception as e:
            print(f"⚠️  Не удалось получить URL туннеля автоматически: {e}")
            print("   Проверьте вручную: http://localhost:4040")

        return True

    except Exception as e:
        print(f"❌ Ошибка настройки ngrok: {e}")
        return False

def verify_ollama():
    """Проверяет работу Ollama"""
    print_step(8, "Проверка работы Ollama")

    try:
        result = run_command(["ollama", "list"], check=True)
        print("✅ Ollama работает корректно")
        print("\n📋 Установленные модели:")
        print(result.stdout)

        # Дополнительная проверка через HTTP
        try:
            import urllib.request
            import json
            req = urllib.request.urlopen(f"http://localhost:{OLLAMA_PORT}/api/tags", timeout=5)
            data = json.loads(req.read().decode())
            models = data.get("models", [])
            print(f"✅ HTTP API работает, доступно моделей: {len(models)}")
        except Exception as e:
            print(f"⚠️  HTTP проверка не прошла: {e}")

        return True
    except Exception as e:
        print(f"❌ Ошибка проверки Ollama: {e}")
        return False

def main():
    """Основная функция"""
    print("\n" + "="*60)
    print("🚀 РАЗВЕРТЫВАНИЕ OLLAMA В GOOGLE COLAB")
    print("="*60 + "\n")

    is_colab = check_colab()

    steps = [
        ("Монтирование Drive", mount_drive if is_colab else lambda: True),
        ("Настройка окружения", setup_ollama_environment),
        ("Установка Ollama", install_ollama),
        ("Запуск сервиса", start_ollama_service),
        ("Загрузка моделей", download_models),
        ("Установка ngrok", install_ngrok),
        ("Настройка туннеля", setup_ngrok_tunnel),
        ("Проверка", verify_ollama),
    ]

    for name, func in steps:
        try:
            if not func():
                print(f"\n⚠️  Шаг '{name}' завершился с предупреждением")
        except Exception as e:
            print(f"\n❌ Шаг '{name}' завершился с ошибкой: {e}")
            print("   Продолжаю выполнение...")

    print("\n" + "="*60)
    print("✅ РАЗВЕРТЫВАНИЕ ЗАВЕРШЕНО")
    print("="*60)
    print("\n📝 Следующие шаги:")
    print("   1. Скопируйте публичный URL из ШАГ 7 выше")
    print("   2. Обновите config.json в вашем проекте:")
    print('      "base_url": "https://ваш-ngrok-url.ngrok-free.app"')
    print("   3. Для API запросов добавляйте заголовок:")
    print('      "ngrok-skip-browser-warning": "true"')
    print("   4. Запустите mafia-analyzer с обновленным конфигом")
    print("\n⚠️  ВАЖНО: Сессия Colab должна оставаться активной!")
    print("   После перезапуска сессии запустите скрипт снова\n")

if __name__ == "__main__":
    main()