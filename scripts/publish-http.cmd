@echo off
chcp 65001 >nul
setlocal enabledelayedexpansion

echo ============================================
echo   MCP для 1С — Публикация HTTP-сервиса
echo ============================================
echo.

:: Поиск платформы 1С
set "FOUND_1C="
for /d %%d in ("C:\Program Files\1cv8\8.*") do set "FOUND_1C=%%d"
for /d %%d in ("C:\Program Files (x86)\1cv8\8.*") do set "FOUND_1C=%%d"

if not defined FOUND_1C (
    echo [ОШИБКА] Платформа 1С не найдена.
    pause
    exit /b 1
)

set "WEBINST=%FOUND_1C%\bin\webinst.exe"
echo Найдена платформа: %FOUND_1C%
echo.

:: Запрос пути к базе
set /p "BASE_PATH=Путь к базе 1С (например C:\1C\Base): "

:: Запрос порта
set /p "PORT=Порт для HTTP-сервиса (по умолчанию 8080): "
if "%PORT%"=="" set "PORT=8080"

echo.
echo Запуск 1С со встроенным HTTP-сервером на порту %PORT%...
echo Нажмите Ctrl+C для остановки.
echo.
echo После запуска проверьте:
echo   curl http://localhost:%PORT%/hs/mcp/metadata
echo.

"%FOUND_1C%\bin\1cv8.exe" ENTERPRISE /F "%BASE_PATH%" /HTTPPort %PORT%
