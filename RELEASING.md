# Releasing

Чек-лист мейнтейнера для выпуска нового релиза mcp-1c.

## Перед тегированием

1. Убедиться, что все PR, которые должны попасть в релиз, смержены.
2. Проверить заголовки смерженных PR: release notes собираются из них автоматически (`generate_release_notes: true` в `.github/workflows/release.yml`). Отдельного `CHANGELOG.md` в репозитории нет.
3. Проверить, что `ConfigurationExtensionCompatibilityMode` в `extension/src/Configuration.xml` соответствует минимально поддерживаемой платформе (сейчас `Version8_3_14`).

## Тегирование и публикация Go-бинарей

1. Создать и запушить тег:
   ```bash
   git tag vX.Y.Z && git push origin vX.Y.Z
   ```
2. Дождаться завершения workflow `.github/workflows/release.yml`. Он соберёт шесть Go-бинарей (три ОС на две архитектуры), посчитает по ним один общий `checksums.txt` и опубликует GitHub Release вместе с этим файлом.
   - Перед публикацией workflow проверяет, что в `dist/` ровно шесть бинарей и что в `checksums.txt` ровно шесть строк. Если это не так, релиз не создаётся.
   - Хэша `.cfe` в этом `checksums.txt` пока нет: расширение собирается вручную, см. следующий раздел.

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

4. Посчитать SHA256. Команда запускается внутри `dist`, чтобы в строке оказалось имя файла без пути: именно в таком виде хэш должен попасть в `checksums.txt`.
   ```bash
   # macOS / Linux
   (cd dist && shasum -a 256 MCP_HTTPService.cfe)

   # Windows (PowerShell)
   Get-FileHash dist\MCP_HTTPService.cfe -Algorithm SHA256
   ```

5. Аттачить к уже существующему релизу:
   ```bash
   gh release upload vX.Y.Z dist/MCP_HTTPService.cfe
   ```

6. Дополнить `checksums.txt` строкой с хэшем `.cfe` и залить заново. Строка должна содержать только имя файла, без пути `dist/`, иначе проверка релиза не пройдёт:
   ```bash
   gh release download vX.Y.Z -p checksums.txt
   (cd dist && shasum -a 256 MCP_HTTPService.cfe) >> checksums.txt
   gh release upload vX.Y.Z checksums.txt --clobber
   ```

7. Добавить в описание релиза блок про установку через Конфигуратор. Сохранить текст ниже в файл `release-block.md`, заменив плейсхолдер `{ВЕРСИЯ_ПЛАТФОРМЫ_СБОРКИ}` на версию платформы, на которой `.cfe` собран фактически:
   ```markdown
   ## Установка расширения 1С через Конфигуратор

   Для пользователей, у которых нет доступа к командной строке на сервере 1С
   (например, при аренде 1С через RDP):

   1. Скачайте `MCP_HTTPService.cfe` (SHA256 в `checksums.txt`).
   2. В Конфигураторе: Конфигурация -> Расширения конфигурации -> Добавить -> «Добавить из файла» -> выбрать `.cfe`.
   3. Убедитесь, что стоит флаг «Активно». Нажмите F7 (Обновить конфигурацию базы данных).

   Расширение объявляет режим совместимости `Version8_3_14`
   (`extension/src/Configuration.xml`), поэтому загружается на 1С:Предприятие
   8.3.14 и выше. Файл собран на платформе 1С {ВЕРСИЯ_ПЛАТФОРМЫ_СБОРКИ}.
   На базах с включёнными профилями безопасности (8.3.21+) перед загрузкой
   может потребоваться снять «Защиту от опасных действий» в свойствах расширения.
   ```

   Дальше этот блок нужно **дописать** к уже существующему описанию, а не подставить вместо него. Причина: `gh release edit --notes-file` заменяет описание целиком, а не дополняет его (`gh release edit --help`: «Read release notes from file», пример подписан «Update the release notes from the content of a file»). При этом описание релиза формируется автоматически из заголовков смерженных PR (`generate_release_notes: true` в `.github/workflows/release.yml`) и служит единственным списком изменений проекта: отдельного `CHANGELOG.md` в репозитории нет. Если передать `--notes-file` с одним только блоком, список изменений будет молча стёрт. Поэтому описание сначала вычитывается, потом дополняется, и только потом записывается обратно:

   ```bash
   # macOS / Linux
   gh release view vX.Y.Z --json body -q .body > release-notes.md &&
     printf '\n\n' >> release-notes.md &&
     cat release-block.md >> release-notes.md
   ```

   ```powershell
   # Windows (PowerShell)
   $body = (gh release view vX.Y.Z --json body -q .body) -join "`n"
   if ($LASTEXITCODE -ne 0) { throw 'не удалось прочитать текущее описание релиза' }
   $notes = $body + "`n`n" + (Get-Content release-block.md -Raw)
   [System.IO.File]::WriteAllText((Join-Path $PWD 'release-notes.md'), $notes, (New-Object System.Text.UTF8Encoding($false)))
   ```

   Здесь важны две детали, и обе неочевидны. Цепочка через `&&` намеренная: если чтение текущего описания не удалось, файл не будет дособран и в релиз ничего не уедет. В PowerShell файл пишется явным вызовом `WriteAllText` в UTF-8 без BOM, а не через `>` или `Set-Content`: в Windows PowerShell 5.1 оператор `>` создаёт файл в UTF-16LE, а `Set-Content` по умолчанию пишет в кодировке `Default` (Windows-1252 для en-US), и описание релиза приедет на GitHub нечитаемым.

   Открыть получившийся `release-notes.md` и убедиться, что в нём есть и автоматический раздел «What's Changed», и новый блок. Только после этого записать описание обратно:
   ```bash
   gh release edit vX.Y.Z --notes-file release-notes.md
   ```

8. Проверить, что в релизе лежит всё, что должно: вкладка Actions, workflow **Verify release assets**, Run workflow, ввести тег `vX.Y.Z`. Прогон падает, если в релизе нет шести бинарей, `checksums.txt` или `MCP_HTTPService.cfe`, а также если хэши в `checksums.txt` не сходятся с реально опубликованными файлами.

## Контрольные точки

- `.cfe` собирается **только локально** на машине мейнтейнера. GitHub Actions платформу 1С не запускает.
- `.cfe` **не коммитится** в репозиторий — только в Releases.
- `gh release edit --notes-file` **заменяет** описание релиза целиком. Автосгенерированный список изменений это единственный changelog проекта, поэтому шаг 7 обязан читать текущее описание и дописывать блок к нему. Сокращать шаг до одного вызова `--notes-file` нельзя.
- Минимальная сборочная платформа должна совпадать с объявленным `ConfigurationExtensionCompatibilityMode` в `extension/src/Configuration.xml`.
- Забытый `.cfe` ловит workflow `.github/workflows/verify-release-assets.yml` (шаг 8 выше). Он запускается вручную и только мейнтейнером: внутри релизного workflow такая проверка была бы красной всегда, потому что на момент сборки тега `.cfe` ещё не загружен. Закрывать связанные issues (#17 и аналогичные) только после зелёного прогона этой проверки.

## Ссылки

- Скрипт сборки: [scripts/build-extension.sh](scripts/build-extension.sh) / [scripts/build-extension.cmd](scripts/build-extension.cmd)
- Документация по установке: [docs/1c-setup.md](docs/1c-setup.md#manual-install)
- Issue, давшее начало процессу: [#17](https://github.com/feenlace/mcp-1c/issues/17)
