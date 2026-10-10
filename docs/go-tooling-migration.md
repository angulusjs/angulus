# План миграции Angulus: Go + esbuild + TSGo

Документ для передачи coding agent. Это план реализации, а не описание уже
доступного API. Основан на состоянии репозитория на 2026-10-10.

### Принятые при реализации решения

- Production tooling реализован в `cmd\angulus`, `internal\project` и
  `internal\compiler`; workspace/release orchestration -- в
  `cmd\angulus-release` и `internal\release`.
- Facades, render и checker files создаются рядом с descriptor и исключены
  из Git. Пользовательский TS остаётся неизменным.
- Минимальный npm launcher сохранён только для выбора native binary,
  аргументов, stdio и сигналов; JS compiler pipeline удалён.
- `@angulus/tooling/vite` и прежний `@angulus/tooling/library` удалены.
  Библиотеки собираются через `angulus build --lib`.
- Новый metadata contract -- `angulus.components.json` v1. Старые
  `.d.ts.angulus.json` sidecars требуют пересборки библиотеки.
- Provider/DI implementation не входит в перенос существующего функционала.
  Поле `providers` отвергается явно; обещанный будущий контракт не является API.
- JSON Schema поставляется с tooling; `$schema` допустим для редакторов.
- Разбор ограниченных выражений и read-only TS declaration facts выполняется
  на Go; semantic types проверяет отдельный native TSGo.
- Сохранены локальные component imports, required inputs, outputs, публичные
  библиотечные aliases/re-exports, lazy ESM, scoped CSS, исходная диагностика
  и source maps. Подробности текущего API находятся в README и docs/libraries.md.

Этот раздел фиксирует решения миграции; исходные этапы ниже оставлены как
критерии реализации, а не свидетельство запуска проверок.

## Текущий статус реализации

Состояние на 2026-10-10: основной app pipeline заменён и demo переведена на
обычные классы с `*.component.json`. Реализованы Go CLI (`check`, `build`,
`serve`, `preview`, `test`), discovery/dependency checks source descriptors,
генерация соседних facade/render файлов с сохранением constructor identity,
Go HTML parser/generator, TSGo native subprocess, browser ESM через публичный
esbuild Go API, scoped CSS через публичный Go CSS parser, polling watcher,
SSE diagnostics overlay/CSS updates, npm platform binaries и browser workflow.
Старые Vite/JS compiler modules и production tooling dependencies удалены.
Workspace build/package/release orchestration перенесена в `cmd/angulus-release`
и `internal/release`: esbuild runtime builds, native TSGo declarations, CLI cross-compiles,
size, pack, version/lockfile updates и OIDC publication preflight. npm остаётся
внешним package manager; Node smoke/isolation scripts остаются test harnesses.

Продолжение реализовано и проверено: library ESM/declarations/versioned metadata
и installed descriptor imports; persistent esbuild contexts и production
minification; CLI config/proxy/test argv/generate и version-1 revisioned NDJSON.
Относительно расширенных критериев ниже остаются ограничения: providers/DI
и multiple named library entrypoints.
Шаблонный checker на native TSGo проверяет используемую публичность членов,
required и typed inputs, typed outputs, DOM property/event types, two-way
values и control-flow narrowing; диагностики привязываются к выражению,
binding или element span в HTML. Expression compiler использует ограниченную
грамматику с lexer и precedence parser, но не полноценную грамматику TypeScript.
Component CSS loads with reachable component modules, including lazy routes.
Source maps compose generated output back to original TS/HTML/CSS. Native TSGo
uses persistent incremental state; esbuild uses incremental contexts.
The restricted expression grammar is intentional. `@angulus/tooling/library` and Vite APIs
are deliberately removed in favor of the native CLI. DI remains unsupported
and descriptor `providers` is rejected, never silently ignored.

## 1. Задание агенту

Перенеси production tooling Angulus с JavaScript/Vite на Go. Используй esbuild
через публичный Go API и отдельный нативный процесс TSGo. Сохрани браузерный
runtime, HTML-синтаксис, типизацию шаблонов, библиотеки компонентов и SPA.

Работай вертикальными проверяемыми этапами. Не ограничивайся каркасом, не удаляй
проверки ради успешной сборки и не оставляй скрытый fallback на старый pipeline.
Не изменяй посторонние пользовательские файлы и не выполняй публикацию пакетов
или создание релиза без отдельного разрешения.

В начале перечитай код, инструкции и этот план. Старый `AGENT.md` требует Vite
и `@Component`; эти конкретные решения заменены новым заданием пользователя.
Обнови соответствующие разделы при реализации, сохранив остальные требования.

## 2. Согласованные требования

- Вся компиляционная и сборочная логика принадлежит Go.
- esbuild встраивается через `github.com/evanw/esbuild/pkg/api`: сборка,
  разрешение runtime-импортов, TS-to-JS, splitting, минификация и ресурсы.
- TSGo отвечает за проверку типов и выпуск деклараций. Не подменяй checker
  транспиляцией esbuild.
- Браузерные пакеты core/router/forms/http остаются TypeScript/JavaScript.
  Не переносить DOM runtime в Go/WASM.
- Результат сборки приложения и библиотек -- ESM. SystemJS не нужен.
  CommonJS-зависимости допустимы только в пределах browser-compatible
  преобразования esbuild; произвольный динамический `require` не обещается.
- Вместо `@Component` используется обычный экспортируемый класс и отдельный
  `*.component.json`.
- Пользовательский TS компонента не переписывается. Компилятор генерирует
  отдельные связующие модули и проверочные файлы.
- Метаданные читаются без исполнения пользовательского TypeScript.
- Синтаксис HTML-шаблонов и текущие ограничения выражений сохраняются.
- Scoped CSS остаётся; зависимости PostCSS и его JS-парсеры удаляются после
  реализации соответствующей обработки на Go.
- Из production pipeline удаляются Vite, JS `typescript` Compiler API,
  `magic-string` и `@jridgewell/sourcemap-codec`.
- Сохраняются CSS updates без потери состояния, reload при изменении TS/HTML,
  браузерная диагностика и инкрементальная работа.
- Не писать собственный универсальный bundler или полноценный TS compiler.
  Дополнительные Go-зависимости для CSS, watch и mappings выбирать обоснованно;
  отсутствие JS tooling dependencies не означает запрет любых Go-библиотек.
- Node/npm допустимы для установки, публикации и тестирования. Playwright,
  happy-dom, tsx и необходимые Node types не удаляются только ради сокращения
  списка зависимостей.

## 3. Новая модель компонента

`counter.ts`:

```ts
import { signal } from "@angulus/core";

export class Counter {
  readonly count = signal(0);

  increment(): void {
    this.count.update(value => value + 1);
  }
}
```

`counter.html`:

```html
<button (click)="increment()">Count: {{ count() }}</button>
@if (count() >= 10) {
  <p>Reached ten.</p>
}
```

Предлагаемая явная схема `counter.component.json`:

```json
{
  "selector": "app-counter",
  "module": "./counter.ts",
  "export": "Counter",
  "template": "./counter.html",
  "styles": ["./counter.css"],
  "imports": [
    "./button.component.json",
    { "module": "@example/widgets", "export": "Badge" }
  ],
  "customElements": []
}
```

Правила:

- Относительные пути разрешаются от descriptor, а не от cwd.
- `imports` задаёт локальные зависимости шаблона, не глобальный registry.
- Строка ссылается на source descriptor; объект -- на публичный экспорт
  скомпилированной библиотеки. Вторую форму окончательно утвердить на этапе 0.
- Для библиотечного компонента selector читается из versioned metadata,
  типы берутся из `.d.ts`, реализация -- из ESM. Не выполнять библиотеку
  во время discovery.
- `customElements` сохраняет текущий явный допуск пользовательских HTML-тегов.
- Неизвестные и повторные JSON-поля, конфликтующие selectors в одном контексте,
  отсутствующие файлы и некорректные значения диагностируются с позициями.
- В первой версии предпочтительны явные поля без неявного угадывания файлов.
  Соглашения об именах можно добавить отдельно.

Generated facade импортирует класс, скомпилированный render и зависимости,
вызывает существующий внутренний `defineComponent` с его реальной сигнатурой
и реэкспортирует тот же класс. Ранее обсуждавшийся `registerComponent` был
псевдокодом: такого API сейчас нет, вводить дубликат без необходимости не надо.

Unit tests могут импортировать исходный класс. Bootstrap, маршруты и публичные
библиотечные exports используют compiled facade. Не менять identity конструктора
и не регистрировать все компоненты eagerly: это сломает lazy loading.

## 4. Решения, требующие согласования до реализации

Это не скрытые допущения. Агент должен согласовать каждый открытый пункт
отдельно до зависящих от него изменений.

| Вопрос | Рекомендуемый вариант |
|---|---|
| Где размещать compiled facades? | Рядом с descriptor как `counter.component.generated.ts`, исключить из Git; генерировать при prepare/check/build/serve. Альтернатива -- `.angulus` с точным контрактом aliases для TSGo и редактора. |
| Допустима ли минимальная JS-обвязка npm? | Оставить launcher только для выбора platform binary, argv/stdio/exit/signals, без сборочной логики. |
| Сохранять ли `@angulus/tooling/library`? | Тонкий process adapter к Go CLI, если JS compatibility shim разрешён; иначе явно удалить API как breaking change. |
| Входит ли DI/providers в первый релиз миграции? | Отдельный этап после основного переноса; не выдавать отсутствующую функциональность за существующую. |
| Как потреблять старые библиотеки? | Versioned metadata и явная политика поддержки v1 либо обязательная пересборка в breaking release. |
| Как сохранить проверки, требующие TS syntax facts? | Ограниченный read-only frontend или публичный parser API; не source transformer и не внутренние пакеты TSGo/esbuild. |

Обычный npm `bin` не предоставляет универсальный OS-conditional launcher.
Если запрещён любой собственный JS, сначала выбрать другой способ установки
platform CLI. Не обещать прежний универсальный npm UX без такого решения.

### Провайдеры

В текущем core нет DI injector, injection tokens и `inject()`. Существующий
`Scope` отвечает за lifecycle/cleanup; экземпляр компонента создаётся через
`new ctor()`. Провайдеры -- новая возможность, не перенос старого механизма.

Предложенный descriptor contract для будущей поддержки:

```json
{
  "providers": {
    "module": "./counter.ts",
    "export": "providers"
  }
}
```

JSON содержит только ссылку. Токены, значения и фабрики остаются обычным TS.
Связующий код статически импортирует экспорт; фабрики выполняются в runtime,
не при компиляции. До реализации DI поле должно выдавать явную ошибку,
а не приниматься с нулевым эффектом.

Если DI включён в задачу, отдельно определить scope/lifetime, parent lookup,
overrides, API injection, доступность при инициализации полей, обработку
отсутствующих токенов/циклов и cleanup. Не добавлять constructor reflection
или application/route hierarchy без согласованного контракта.

## 5. Карта текущего кода

Пути относительно корня репозитория.

| Область | Точки входа |
|---|---|
| Go HTML parser и генератор | `internal\compiler\parser.go`, `generator.go`, `ast.go` |
| Текущий compiler process | `cmd\angulus-compiler\main.go`, `internal\compiler\protocol.go`, `packages\tooling\src\client.mjs` |
| Декораторы, импорты, library metadata | `packages\tooling\src\frontend.mjs` |
| Выражения и name binding | `packages\tooling\src\expressions.mjs` |
| Template semantics и typecheck shims | `packages\tooling\src\templates.mjs` |
| Project graph, TSGo, mappings | `packages\tooling\src\project.mjs` |
| Scoped CSS | `packages\tooling\src\styles.mjs` |
| CLI, dev server, сборка | `packages\tooling\src\cli.mjs`, `vite.mjs` |
| Библиотеки и declarations | `packages\tooling\src\library.mjs` |
| Runtime и маршруты | `packages\core\src\index.ts`, `packages\router\src\index.ts` |
| Packaging и releases | `scripts`, manifests пакетов, `.github\workflows` |

## 6. Целевой pipeline

```text
component.json + TS + HTML + CSS
                |
      Go project graph / metadata
                |
  Go HTML + expressions + scoped CSS compiler
                |
   generated facades / render / typecheck shims
              /   \
       esbuild     TSGo native executable
          |          |
     ESM/CSS/maps    diagnostics / .d.ts
              \   /
     Go CLI, dev server, library builder
```

TSGo запускается напрямую как native executable. Закреплённый npm-пакет
содержит Node launcher и platform packages; не запускать `bin/tsgo` через Node
из Go по привычке. Проверить расположение бинарника на каждой платформе,
лицензию, версию и поведение CLI. Не полагаться на `unstable` JS API или
импортируемость внутренних Go-пакетов.

## 7. Этапы реализации

### Этап 0. Контракты и технические проверки

- Снять карту текущего поведения по коду и тестам; разделить сохраняемое
  поведение, согласованные breaking changes и открытые вопросы.
- Согласовать раздел 4, descriptor schema, библиотечные ссылки и версионирование
  source metadata, library metadata и compiler/runtime ABI.
- Проверить закреплённый TSGo: diagnostics, incremental, declaration-only emit,
  tsconfig inheritance, отмену и ненулевые exit codes.
- Выбрать публичные Go API для esbuild, CSS parsing, watch и mappings.
  Проверить лицензии и совместимость с текущей cross-platform distribution.
- Доказать минимальными сценариями, что возможно сохранить input/output
  contracts, library exports и переносимые declarations без JS Compiler API.

Приёмка: у каждой критической обязанности старого pipeline есть конкретный
исполнитель. Нет обещаний получить AST из esbuild или TSGo CLI, если такого
интерфейса нет.

### Этап 1. Go CLI и первый вертикальный компонент

- Добавить Go CLI и project layer. Переиспользовать текущий Go parser/generator
  in-process вместо обязательного внутреннего IPC.
- Реализовать descriptor parsing, позиции ошибок и dependency graph.
- Задать правила discovery JSON отдельно от TS `include/exclude`.
- Генерировать facade, render и зависимости без изменения исходного TS.
- Подключить esbuild и довести пример счётчика от JSON до работающего браузера.
- Добавить prepare/generation lifecycle: чистый checkout должен получать
  generated modules до typecheck, editor usage и package build.
- Удалять только собственные устаревшие артефакты по явному учёту файлов;
  не перезаписывать пользовательские файлы при совпадении имени.

Приёмка: обычный класс + JSON + внешний HTML/CSS работают; constructor identity
сохранена; отсутствующий export или неверный constructor обнаруживаются;
исходники не выполняются при discovery и не переписываются.

### Этап 2. Выражения, template semantics и TSGo

- Перенести существующую ограниченную грамматику выражений на Go.
  Использовать lexer и AST/precedence parser, не регулярки по исходной строке.
- Сохранить приоритеты операторов, optional chaining, вызовы, литералы,
  массивы, object shorthand и существующие запреты.
- Различать имена свойств и свободные идентификаторы, квалифицировать `ctx`,
  сохранять `this`, shadowing, `$index`, event-only `$event`.
- Различать plain track locals и signal locals в исполняемом шаблоне.
- Генерировать runtime expressions и checker expressions из общего анализа.
- Перенести проверки неизвестных компонентов, custom elements и bindings.
- Генерировать настоящие TS control-flow конструкции для narrowing в
  `@if`, `@switch`, `@for`.
- Проверять `input`, `input.required`, `output`, inherited required inputs,
  static/dynamic bindings, payload событий, DOM events/properties,
  публичность используемых членов и two-way binding.
- Реализовать прямой TSGo adapter, сериализацию incremental checks,
  отмену, обработку crash/malformed diagnostics и актуальность revision.
- Сохранить tsconfig пользователя; не заменять его молчаливой конфигурацией.
- Реализовать общий mapping layer: UTF-8 offsets Go, UTF-16 TS,
  исходные позиции HTML/TS/JSON и generated spans.

Приёмка: ошибки в неоткрытых lazy-компонентах выявляются полным check/build;
ошибки типов ведут к исходному HTML; Unicode, CRLF и Windows paths не смещают
диагностику; сбой TSGo не превращается в успешную проверку.

Важная граница: JSON устраняет разбор декораторов, но не все TS syntax facts.
Текущий код запрещает private/protected поля с input/output initializer,
даже если они не используются в HTML. Обычные instance types не дают полной
информации для этой проверки. Аналогично нужно закрыть aliases/re-exports
и module specifiers в declarations. Реализуй read-only syntax analysis
или согласуй явное изменение контракта; не теряй проверки молча.

### Этап 3. Scoped CSS и source maps

- Использовать полноценный разбор CSS selectors, values и at-rules.
- Сохранить scoping compound selectors и положение атрибута до pseudo-element.
- Сохранить переименование keyframes и animation references, диагностику
  запрещённых scoped `@import`, nesting, `:host`, `:global`, `:root`
  и неоднозначных animation shorthand.
- Для `styles[]` сохранить порядок, общий scope и согласованное переименование
  анимаций между файлами.
- Определить стабильный scope ID, независимый от абсолютного пути checkout.
- Передавать CSS assets esbuild с правильной исходной директорией.
  Удалить Vite-specific `/@fs/`.
- Передавать карты преобразований в esbuild и корректно связывать их
  с итоговыми maps. Не считать maps бандлера заменой HTML/CSS mappings.

Приёмка: инкапсуляция и анимации не меняют семантику; ресурсы работают после
сборки и из библиотек; карты указывают на исходные TS/HTML/CSS.

### Этап 4. Полноценная сборка приложений и библиотек

- Добавить обработку HTML entry, module scripts, CSS links, public assets,
  base URL и итоговых hashed output paths в пределах текущего контракта.
- Сборку приложения выполнять с ESM и поддержкой lazy chunks.
- Сохранить `build --lib`, публичные entrypoints, named exports/aliases,
  peer dependencies и externalization зависимостей вместе с subpaths.
- Генерировать `.d.ts` через TSGo; обеспечить переносимые specifiers,
  отсутствие абсолютных путей и ссылок на локальные generated check files.
- Выпускать versioned component metadata и scoped stylesheet.
- Согласовать runtime resolution esbuild и type resolution TSGo:
  package exports/imports, `types` conditions, tsconfig paths/extends,
  workspace links, `.js` specifiers на TS sources.
- Проверять полный descriptor set, а бандлить достижимый runtime graph.
- Публиковать локальный результат сборки из staging только после успешного
  check/emit/build. Не оставлять частичный результат под видом успешного dist.

Приёмка: установленная библиотека работает без своих исходников;
required inputs/outputs проверяются через её публичные aliases;
runtime core не дублируется; lazy routes не становятся eager;
typecheck shims и dev client не попадают в production.

### Этап 5. Dev server, CLI и миграция demo

- Использовать долгоживущий esbuild context и Go watcher с add/change/delete.
- Разделить graph компиляции/проверок и graph бандлинга; учитывать внешние
  шаблоны, стили, metadata и библиотечные контракты.
- Согласовывать check/build/browser events через revision IDs.
- Реализовать dev-only клиент CSS updates, reload и error overlay.
- CSS-only изменения не должны повторно исполнять компонентный JS.
  Если изменился JS graph/output, выполнить reload.
- Не очищать актуальную ошибку устаревшим успешным результатом.
- Сохранить localhost default, proxy, ошибку занятого порта, preview,
  JSON diagnostics/events и navigation-only SPA fallback.
- Сохранить `test` как запуск настроенных тестов приложения.
- Генератор создаёт TS/JSON/HTML/CSS/test и не затирает существующие файлы.
- Перевести demo/bootstrap/lazy routes на compiled facade imports.
- При shutdown закрывать context, watcher, connections и TSGo.

Приёмка: CSS edit сохраняет состояние; TS/HTML edit перезагружает страницу;
ошибка lazy template видна без посещения маршрута; вложенный URL работает;
отсутствующие JS/CSS и `/api` не возвращают SPA HTML; после shutdown
не остаются дочерние процессы. HTTP readiness не равна успешному typecheck.

### Этап 6. Дистрибуция, зависимости и документация

- Перенести сборочную логику scripts в Go, сохранив удобные npm entrypoints.
  Минимальные согласованные launcher/process adapters не содержат pipeline.
- Сохранить prebuilt platform binaries, exact version coupling,
  понятные ошибки отсутствующей или несовместимой platform dependency.
- Перенести package build/size и, в рамках полного переноса инструментария,
  pack/version/release orchestration. npm остаётся внешним package manager.
- Сохранить release preflight, integrity, порядок публикации,
  повторный запуск после частичного релиза и OIDC/provenance.
  Не выполнять настоящую публикацию при разработке миграции.
- Перевести tooling tests с приватных JS imports на Go unit tests и CLI
  black-box tests. Сохранить смысл существующих regression cases.
- Обновить CI, isolated package smoke, browser assertions и Windows coverage.
- Удалить старые production modules/dependencies после достижения parity,
  обновить lockfiles. Не оставлять второй неявный путь сборки.
- Удалить `@angulus/tooling/vite`; судьбу `./library` выполнить по решению
  этапа 0. Оформить intentional breaking changes и migration guide.
- Обновить `AGENT.md`, README, compiler/library/release docs, схемы,
  package descriptions, examples и генератор.

Приёмка: чистая установка из tarballs работает без Go toolchain и зависимости
от workspace; build/check/serve не вызывают старый JS pipeline; документирован
переход с декораторов и старых библиотечных metadata.

## 8. Проверки и критерии готовности

Используй существующие runners и подходящие минимальные selectors на каждом
этапе. Новые тесты добавляй в них. Не устанавливай зависимости без необходимости
и не удаляй regression coverage из-за отказа от внутренних JS API.

Точки опоры:

- `internal\compiler\parser_test.go`, `generator_test.go`, `protocol_test.go`;
- `packages\tooling\test\frontend.test.mjs`, `checker.test.mjs`,
  `library.test.mjs`, `cli.test.ts`, package/binary/publish/isolation tests;
- тесты core/router/forms/http;
- `tests\browser\workflow.spec.ts`;
- `.github\workflows\ci.yml`, `release.yml`.

Текущие entrypoints, которые нужно сохранить или явно мигрировать:

```text
go test ./...
npm run typecheck
npm run check
npm test
npm run build
npm run test:browser
npm run size
npm run packages:pack
npm run packages:smoke
```

Дополнительные обязательные сценарии:

- Чистый checkout и генерация facades до TS checking.
- Descriptor add/delete/rename, ошибочные поля, cycles и несовместимые
  definitions одного constructor.
- Сигналы, правильный `this`, required inputs, outputs, keyed reconciliation,
  lifecycle и disposal без изменения поведения.
- Ошибки в неоткрытых lazy routes, сохранение narrowing и позиции после
  кириллицы/emoji, с CRLF и пробелами в пути.
- Несколько CSS-файлов, keyframes, assets и CSS lazy chunks.
- Библиотечные aliases/re-exports и установленный пакет без source files.
- Stale rebuild/check, занятый порт, ошибка TSGo и корректное завершение.
- Изолированный npm install без Go и без ancestor dependency leakage.
- Размер минимального runtime: сохранить имеющийся ориентир до 10 KiB gzip
  без роутера, приложения и dev-кода; сообщить фактическое измерение.

Миграция завершена только когда новый pipeline самостоятельно обеспечивает
все сохранённые контракты, а отклонения явно согласованы и документированы.
Провайдеры не объявлять реализованными, если добавлена лишь ссылка в JSON.

В итоговом отчёте агент перечисляет реализованные этапы, breaking changes,
команды запуска, результаты проверок, размер runtime и оставшиеся ограничения.
При блокере сохраняет рабочий воспроизводимый этап и описывает точную причину,
а не подменяет функциональность заглушкой.
