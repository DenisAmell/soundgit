# Reaper `.rpp` ↔ IR

Что адаптер читает в типизированные поля IR. Всё, чего нет в таблицах, сохраняется как непрозрачная запись
(`ext.layout`) у сущности-владельца и возвращается в файл без изменений.

> Таблицы составлены по синтетическому корпусу (`testdata/rpp/synthetic`), написанному вручную.
> Сверка с проектами, сохранёнными из Reaper, — задачи 1.3–1.5.

## Идентификаторы

| Сущность | Источник | Если отсутствует |
|---|---|---|
| Дорожка | `TRACKID`, иначе аргумент `<TRACK {GUID}` | `track:<порядковый номер>` |
| Элемент | `IGUID` | `item:<id дорожки>:<номер>` |
| Тейк | `GUID` в сегменте тейка | `take:<id элемента>:<номер>` |
| Эффект | `FXID` | `fx:<id дорожки>:<номер>` |
| Огибающая | `EGUID` | `env:<id владельца>:<цель>` |

Повтор идентификатора у двух сущностей одного вида — ошибка разбора.

## Проект (`<REAPER_PROJECT`)

| `.rpp` | IR |
|---|---|
| 2-й аргумент заголовка | `meta.dawVersion` |
| `SAMPLERATE a` | `meta.sampleRate` |
| `TEMPO bpm num den` | `tempo.bpm`, `tempo.num`, `tempo.den` |
| `MARKER idx time name flags` | `markers[]`: `index`, `time`, `name`, `region` (бит 0 флагов) |
| `<TEMPOENVEX` | огибающая с целью `tempo` (темп-карта) |
| `<TRACK` | `tracks`, порядок — `trackOrder` |

## Дорожка (`<TRACK`)

| `.rpp` | IR |
|---|---|
| `NAME` | `name` |
| `PEAKCOL` | `color` |
| `VOLPAN vol pan` | `volume`, `pan` |
| `MUTESOLO mute solo` | `mute`, `solo` |
| `IPHASE` | `phase` |
| `ISBUS role depth` | `folderDepth` (= `depth`; `role` выводится из него при записи) |
| `AUXRECV src mode vol pan` | `receives[]`: `srcTrackId` (номер дорожки → её идентификатор), `mode`, `volume`, `pan` |
| `<FXCHAIN` | `fx`, порядок — `fxOrder` |
| `<ITEM` | `items`, порядок в файле — `itemOrder` |
| блоки `*ENV`, `*ENV2`, `*ENV3` | `envelopes` |

Посыл в Reaper хранится на принимающей дорожке и ссылается на источник по номеру. В IR ссылка — по
идентификатору, поэтому перестановка дорожек не ломает посылы: номер пересчитывается при записи.

## Цепочка эффектов (`<FXCHAIN`)

Цепочка плоская: каждая строка `BYPASS` начинает группу строк одного эффекта.

| `.rpp` | IR |
|---|---|
| `BYPASS bypass offline` | `bypass`, `offline` |
| блок плагина `<VST`, `<AU`, `<JS`, `<CLAP`, … | `format`, `name`, `ident`; содержимое блока — `state` (хранится отдельным объектом, в модели — `stateHash`) |
| `FXID` | `id` |
| `<PARMENV param …` | огибающая с целью `param:<param>` |

`format` для `<VST` берётся из начала имени (`VST3:`, `VSTi:` …). `ident`: для VST — уникальный идентификатор
плагина (5-й аргумент) или имя файла; для AU — тип/подтип/производитель; для JS — путь скрипта.

## Огибающая

| `.rpp` | IR |
|---|---|
| тег блока | `target`: `VOLENV2`→`volume`, `VOLENV`→`volume-prefx`, `VOLENV3`→`volume-trim`, `PANENV2`→`pan`, `PANENV`→`pan-prefx`, `WIDTHENV2`→`width`, `WIDTHENV`→`width-prefx`, `MUTEENV`→`mute`, `TEMPOENVEX`→`tempo`, `MASTERPLAYSPEEDENV`→`playrate`, прочие → `reaper:<тег>` |
| `EGUID` | `id` |
| `ACT a` | `active` |
| `PT time value shape …` | `points[]`: `t`, `v`, `s`; остальные значения строки сохраняются в `raw` точки |

## Элемент (`<ITEM`) и тейки

Атрибуты первого тейка лежат прямо в элементе; каждый следующий тейк начинается строкой `TAKE`
(`TAKE SEL` — активный).

| `.rpp` | IR |
|---|---|
| `POSITION`, `LENGTH` | `position`, `length` |
| `LOOP` | `loop` |
| `FADEIN _ len`, `FADEOUT _ len` | `fadeIn`, `fadeOut` |
| `MUTE` | `mute` |
| `IGUID` | `id` |
| `TAKE SEL` | `activeTake` |
| `NAME` | `takes[].name` |
| `SOFFS` | `takes[].offset` |
| `PLAYRATE rate _ pitch` | `takes[].playrate`, `takes[].pitch` |
| `GUID` | `takes[].id` |
| `<SOURCE type` | `takes[].source.type` |
| `FILE path` | `source.file` — путь с `/` и в Unicode NFC |
| вложенный `<SOURCE` (секция) | `source.inner` |
| `HASDATA _ ppq` | `source.midi.ppq` |
| `E`, `e`, `Em`, `em`, `X`, `x`, блоки `<X` | `source.midi.events[]` — в нотации Reaper, по порядку |

Строки `NAME`, `VOLPAN`, `SOFFS`, `PLAYRATE`, `CHANMODE`, `GUID`, `RECPASS`, `TAKECOLOR`, `TAKEVOLPAN`,
`TMINFO`, `TAKEMARKER`, `SM`, блоки `<SOURCE`, `<TAKEFX` и огибающие внутри элемента относятся к тейку.

## Состояние редактора

Эти атрибуты сохраняются и восстанавливаются, но их изменение не считается изменением проекта
(не показывается в `status` и `diff` без `--view`, не создаёт новую версию):

`CURSOR`, `ZOOM`, `VZOOMEX`, `SELECTION`, `SELECTION2`, `SEL`, `TRACKHEIGHT`, `WNDRECT`, `SHOW`, `LASTSEL`,
`DOCKED`, `FLOATPOS`, `FLOAT`, `VIS`, `LANEHEIGHT`, `MIXERUIFLAGS`, `SHOWINMIX`, `MASTERTRACKHEIGHT`,
`MASTERTRACKVIEW`, `MASTER_SEL`, `TRACKSEL`, `CFGEDIT`, `CFGEDITVIEW`, `EVTFILTER`.

Заголовок проекта (версия Reaper и отметка времени сохранения) тоже не сравнивается.

## Запись

- Строка с типизированными полями выводится в исходном виде, пока её поля читаются так же; иначе
  заменяются только изменившиеся значения. `0.50` и `0.5` — одно и то же значение.
- Дочерние сущности одного вида (дорожки, элементы, эффекты, тейки, маркеры, посылы) выводятся вместе на
  месте первой из них, в порядке модели.
- Отступы — два пробела на уровень; окончания строк, завершающий перевод строки и BOM — как в исходном файле.

## Не поддерживается на этапе 1

- Сущности, созданные не из файла (без исходного `layout`): адаптер запишет только известные ему строки.
- Входная цепочка эффектов `<FXCHAIN_REC`, эффекты тейка `<TAKEFX`, контейнеры эффектов — как непрозрачные данные.
- Параметры внутри состояния плагина.
