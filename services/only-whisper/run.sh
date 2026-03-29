#!/usr/bin/env bash
# Чанки:   ./run.sh file <URL> <ПУТЬ_К_ФАЙЛУ> <СПИКЕРЫ>
# Целиком: go run main.go ingest <URL> <ПУТЬ_К_ФАЙЛУ> [СПИКЕРЫ]
exec go run main.go "$@"
