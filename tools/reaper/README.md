# Скрипты для Reaper

Запускаются в **отдельном** экземпляре Reaper (`-newinst`), уже открытый Reaper не затрагивается.
Экземпляр после работы скрипта не закрывается сам — закройте его или завершите процесс.

## Пересоздать тестовый корпус

```bash
SOUNDGIT_CORPUS_OUT="$PWD/testdata/rpp/minimal" /Applications/REAPER.app/Contents/MacOS/REAPER -newinst -nosplash -new "$PWD/tools/reaper/make-corpus.lua"
```

Каталог должен существовать и быть пустым. Ход работы пишется в `make-corpus.log` в этом каталоге; последняя
строка `DONE` — признак успеха (файл журнала в репозиторий не кладётся).

## Проверить, что Reaper принимает файлы, записанные SoundGit

```bash
cd core && SOUNDGIT_REAPER=/Applications/REAPER.app/Contents/MacOS/REAPER go test ./internal/adapters/reaper -run Survives -v
```

Тест правит модель проекта, записывает файл, открывает его в Reaper, просит Reaper сохранить (`resave.lua`) и
сверяет, что Reaper прочитал те же значения. Без переменной `SOUNDGIT_REAPER` тест пропускается.
