# Releasing

Чек-лист мейнтейнера для выпуска нового релиза mcp-1c.

## Перед тегированием

1. Убедиться, что все PR, которые должны попасть в релиз, смержены.
2. Обновить `CHANGELOG.md` (если ведётся) или подготовить release notes.
3. Проверить, что `ConfigurationExtensionCompatibilityMode` в `extension/src/Configuration.xml` соответствует минимально поддерживаемой платформе (сейчас `Version8_3_14`).

## Тегирование и публикация Go-бинарей

1. Создать и запушить тег:
   ```bash
   git tag vX.Y.Z && git push origin vX.Y.Z
   ```
2. Дождаться завершения workflow `.github/workflows/release.yml` — он соберёт Go-бинари для всех платформ и создаст GitHub Release с `checksums.txt`.

## Сборка и публикация .cfe расширения

`.cfe` собирается **только локально** на машине мейнтейнера: GitHub Actions платформу 1С не запускает (лицензионные ограничения).

1. На локальной машине с установленной платформой 1С 8.3.14 (минимально поддерживаемая версия) создать пустую файловую ИБ, если её ещё нет:
   ```bash
   # macOS
   "/Applications/1cv8.localized/8.3.14.xxx/1cv8.app/Contents/MacOS/1cv8" \
       CREATEINFOBASE File="/tmp/mcp-build-ib-8314"

   # Windows
   "C:\Program Files\1cv8\8.3.14.xxx\bin\1cv8.exe" ^
       CREATEINFOBASE File="C:\temp\mcp-build-ib-8314"
   ```
   (фактический путь к бинарю и номер сборки 8.3.14 подставить из системы)

2. Собрать `.cfe`:
   ```bash
   # macOS / Linux
   ./scripts/build-extension.sh /tmp/mcp-build-ib-8314 ./dist/MCP_HTTPService.cfe

   # Windows
   scripts\build-extension.cmd C:\temp\mcp-build-ib-8314 dist\MCP_HTTPService.cfe
   ```
   Если установлено несколько версий платформы — скрипт спросит, какую использовать; выбрать **8.3.14**.

3. Проверить сборку: открыть `./dist/MCP_HTTPService.cfe` в чистой 1С 8.3.14 (минимальная) и в актуальной версии (например 8.3.27) — убедиться, что грузится и активируется без ошибок, F7 проходит чисто.

4. Посчитать SHA256:
   ```bash
   # macOS / Linux
   shasum -a 256 dist/MCP_HTTPService.cfe

   # Windows (PowerShell)
   Get-FileHash dist\MCP_HTTPService.cfe -Algorithm SHA256
   ```

5. Аттачить к уже существующему релизу:
   ```bash
   gh release upload vX.Y.Z dist/MCP_HTTPService.cfe
   ```

6. Обновить `checksums.txt` — добавить строку с хэшем `.cfe` и залить заново:
   ```bash
   gh release download vX.Y.Z -p checksums.txt
   echo "<hash>  MCP_HTTPService.cfe" >> checksums.txt
   gh release upload vX.Y.Z checksums.txt --clobber
   ```

7. В описании релиза (через `gh release edit vX.Y.Z --notes-file release-notes.md` или редактирование на сайте) добавить блок:
   ```markdown
   ## Установка расширения 1С через Конфигуратор

   Для пользователей, у которых нет доступа к командной строке на сервере 1С
   (например, при аренде 1С через RDP):

   1. Скачайте `MCP_HTTPService.cfe` (SHA256 в `checksums.txt`).
   2. В Конфигураторе: Конфигурация -> Расширения конфигурации -> Добавить -> «Добавить из файла» -> выбрать `.cfe`.
   3. Убедитесь, что стоит флаг «Активно». Нажмите F7 (Обновить конфигурацию базы данных).

   Собрано на платформе 1С 8.3.14, работает на 8.3.14 и выше. На базах с включёнными
   профилями безопасности (8.3.21+) перед загрузкой может потребоваться снять
   «Защиту от опасных действий» в свойствах расширения.
   ```

## Контрольные точки

- `.cfe` собирается **только локально** на машине мейнтейнера. GitHub Actions платформу 1С не запускает.
- `.cfe` **не коммитится** в репозиторий — только в Releases.
- Минимальная сборочная платформа должна совпадать с объявленным `ConfigurationExtensionCompatibilityMode` в `extension/src/Configuration.xml`.
- Если `.cfe` забыт — никаких автоматических CI-проверок не сработает. Это ответственность мейнтейнера. Закрытие связанных issues (#17 и аналогичных) — после публикации `.cfe` к релизу.

## Ссылки

- Скрипт сборки: [scripts/build-extension.sh](scripts/build-extension.sh) / [scripts/build-extension.cmd](scripts/build-extension.cmd)
- Документация по установке: [docs/1c-setup.md](docs/1c-setup.md#manual-install)
- Issue, давшее начало процессу: [#17](https://github.com/feenlace/mcp-1c/issues/17)
