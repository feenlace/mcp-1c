@echo off
chcp 65001 >nul
setlocal enabledelayedexpansion

echo ============================================
echo   MCP для 1С — Установка расширения
echo ============================================
echo.

:: Поиск платформы 1С
set "FOUND_1C="
for /d %%d in ("C:\Program Files\1cv8\8.*") do set "FOUND_1C=%%d"
for /d %%d in ("C:\Program Files (x86)\1cv8\8.*") do set "FOUND_1C=%%d"

if not defined FOUND_1C (
    echo [ОШИБКА] Платформа 1С не найдена.
    echo Установите 1С:Предприятие и повторите попытку.
    pause
    exit /b 1
)

set "DESIGNER=%FOUND_1C%\bin\1cv8.exe"
echo Найдена платформа: %FOUND_1C%
echo.

:: Путь к расширению
set "SCRIPT_DIR=%~dp0"
set "CFE_PATH=%SCRIPT_DIR%..\extension\MCP_HTTPService.cfe"

if not exist "%CFE_PATH%" (
    echo [ОШИБКА] Файл расширения не найден: %CFE_PATH%
    echo Скачайте его из GitHub Releases.
    pause
    exit /b 1
)

echo Файл расширения: %CFE_PATH%
echo.

:: Запрос пути к базе
set /p "BASE_PATH=Введите путь к базе 1С (например C:\1C\Base): "

if not exist "%BASE_PATH%\1Cv8.1CD" (
    echo [ОШИБКА] Файл базы 1Cv8.1CD не найден в %BASE_PATH%
    echo Проверьте путь и повторите.
    pause
    exit /b 1
)

echo.
echo Установка расширения в базу: %BASE_PATH%
echo.

:: Загрузка расширения
"%DESIGNER%" DESIGNER /F "%BASE_PATH%" /LoadCfg "%CFE_PATH%" /Extension "MCP_HTTPService" /UpdateDBCfg
if %errorlevel% neq 0 (
    echo.
    echo [ОШИБКА] Не удалось загрузить расширение. Код: %errorlevel%
    echo Убедитесь, что база не открыта в Конфигураторе или Предприятии.
    pause
    exit /b 1
)

echo.
echo ============================================
echo   Расширение MCP_HTTPService установлено!
echo ============================================
echo.
echo Следующий шаг — опубликовать HTTP-сервис:
echo   1. Откройте базу в Конфигураторе (от администратора)
echo   2. Администрирование → Публикация на веб-сервере
echo   3. Убедитесь что MCPService отмечен
echo   4. Нажмите "Опубликовать"
echo.
echo Или запустите с встроенным веб-сервером:
echo   "%DESIGNER%" ENTERPRISE /F "%BASE_PATH%" /HTTPPort 8080
echo.
pause
