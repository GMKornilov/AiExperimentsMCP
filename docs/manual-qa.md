# Manual QA — подготовка прогона

Дата: 2026-09-27. Спецификация: `SPEC.md`, `docs/api.md`, `README.md`.

Средство реального UI-прогона: браузер через CUA. Фактический прогон 2026-09-27 заблокирован до навигации: CUA не предоставляет browser provider (ответ `Browser is not available`), а подключение к Chrome прерывается ошибкой ScreenCaptureKit `-3811` при захвате экрана. Поэтому ниже только тест-кейсы — фактических результатов и скриншотов приложения нет. UI-задачи и QA-расписания не создавались.

## Фактический результат (частичный)

Для изолированного fallback-прогона Playwright создана одна demo Task «Покажи текущее время через demo get_time».

| Case | Result | Evidence |
|---|---|---|
| TC-web-qa-01 | PASSED | `/private/tmp/agent-loop-qa-task-created.png`: статус «Готово», план, event инструмента `get_time`, результат и лента из 5 событий видимы. |

Остальные кейсы в этом файле не следует считать выполненными: их UI-автоматизация передана отдельному test-agent, а CUA недоступен.

## Smoke + Feature

### TC-web-qa-01: demo-задача показывает полный цикл

Tier: Smoke + Feature  
Target: desktop browser  
Preconditions: demo MCP и Agent Loop доступны; UI открыт.  
Steps: создать задачу с prompt «Покажи текущее время через demo get_time». Дождаться терминального статуса.  
Expected Result: создана Task; видны prompt, план, события planning/execution/tool/result, финальный result; выполнен вызов `get_time`.  
Spec Reference: SPEC §2, docs/api.md Task.

### TC-web-qa-02: инструменты demo доступны и echo исполняется

Tier: Feature  
Target: desktop browser  
Preconditions: как в TC-web-qa-01.  
Steps: открыть MCP Tools; проверить `echo`, `get_time`; создать задачу «Попроси demo echo повторить QA ping».  
Expected Result: Tools содержит оба исходных имени; задача завершается, события и результат отражают вызов `echo` с `QA ping`.  
Spec Reference: SPEC §2, docs/api.md `/api/tools`.

### TC-web-qa-03: one-shot расписание создаёт новую полноценную Task

Tier: Smoke + Feature  
Target: desktop browser  
Preconditions: demo-сервис доступен; текущее время известно.  
Steps: создать Schedule с уникальным name, PROMPT «Покажи текущее время через demo get_time», `run_at` в ближайшую минуту. Дождаться запуска; открыть Run и связанную Task.  
Expected Result: Schedule создан; Run содержит `task_id`; переход по нему открывает новую Task с планом, execution, tool-event `get_time` и result.  
Spec Reference: SPEC §3–4, docs/api.md Schedule/Run.

### TC-web-qa-04: recurring расписание можно приостановить, возобновить и удалить

Tier: Feature  
Target: desktop browser  
Preconditions: UI открыт.  
Steps: создать cron Schedule с уникальным name и допустимым пятичастным cron; pause, resume, delete.  
Expected Result: enabled/status корректно меняются после pause/resume; Schedule пропадает после delete. По окончании прогона активных QA cron jobs нет.  
Spec Reference: SPEC §3–4, docs/api.md PATCH/DELETE schedules.

### TC-web-qa-05: required-поля и время валидируются

Tier: Feature  
Target: desktop browser  
Preconditions: UI открыт.  
Steps: отправить пустую Task; отправить Schedule без name, без PROMPT, без времени; отправить Schedule с одновременно `run_at` и cron либо невалидными значениями, если UI предоставляет ввод.  
Expected Result: отправка блокируется или показана ясная inline/API ошибка; некорректная сущность не создаётся.  
Spec Reference: docs/api.md POST tasks/schedules.

### TC-web-qa-06: навигация и состояния Task

Tier: Feature  
Target: desktop browser  
Preconditions: создана demo-задача.  
Steps: открыть Task из списка, вернуться назад, обновить страницу во время/после выполнения.  
Expected Result: Task доступна из списка и после refresh; события, plan и result не теряются; back возвращает к ожидаемому списку.  
Spec Reference: SPEC §4, docs/api.md GET tasks.

### TC-web-qa-07: ошибка недоступного API отображается в UI

Tier: Feature  
Target: desktop browser  
Preconditions: безопасный способ открыть UI при недоступном API.  
Steps: инициировать загрузку данных при недоступном API, не меняя конфигурацию и не затрагивая внешние сервисы.  
Expected Result: UI показывает понятную ошибку без пустого/зависшего экрана.  
Spec Reference: docs/api.md Errors; heuristic «видимость состояния системы».

### TC-web-qa-08: responsive и лёгкая accessibility-проверка

Tier: Smoke + Feature  
Target: desktop и viewport 390 px  
Preconditions: UI открыт.  
Steps: проверить Task, Tools, Schedule list/form на desktop и 390 px; пройти Tab/Shift+Tab по форме и отправить с клавиатуры.  
Expected Result: нет горизонтального overflow; формы и кнопки доступны с клавиатуры, фокус видим; интерактивные элементы имеют доступные имена; touch-цели не выглядят тесными; явных проблем контраста нет.  
Spec Reference: heuristic; требования QA §5.

## Ограничения

Реальные внешние LLM, ключи и Telegram не используются и не проверяются. В ходе фактического прогона фиксируются только наблюдаемые результаты CUA со скриншотом или accessibility snapshot для каждого FAILED/BLOCKED.
