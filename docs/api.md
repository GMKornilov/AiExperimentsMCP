# HTTP API

JSON, timestamps RFC3339. Ошибки: `{ "error": "описание" }`. Все запросы записи требуют `Content-Type: application/json` (кроме DELETE без тела) и, если передан `Origin`, совпадения с адресом запроса. API локальный, без аутентификации.

Конфигурация YAML: верхний уровень `listen`, `database`, `timezone`, `task_timeout`, `mcp_servers: [{name,url,headers?}]`; блок `llm: {provider,service,model,base_url,api_key,temperature?,max_iterations?}`. `provider`: `openai-compatible` либо явный `demo`. `base_url` — префикс для `/chat/completions`. `${NAME}` подставляется после чтения `--env-file`, а уже заданные переменные окружения имеют приоритет. URL MCP не допускает userinfo, query и fragment; ключи передаются в `headers`. `/api/config` скрывает путь MCP URL и все ключи.

| Метод | Путь | Ответ |
|---|---|---|
| GET | `/api/health` | `{ "status":"ok" }` |
| GET | `/api/config` | `{ "model", "provider", "temperature", "max_iterations", "timezone", "mcp_servers":[{"name","url"}] }`; секреты исключены |
| GET | `/api/tools` | `{ "servers":[{"name","status","error"?}], "tools":[{"name","server","description","input_schema"}] }`; `name` — исходное имя MCP |
| GET | `/api/tasks` | массив Task |
| POST | `/api/tasks` | `{ "prompt":"..." }` → Task, 202 |
| GET | `/api/tasks/{id}` | Task |
| POST | `/api/tasks/{id}/cancel` | Task |
| GET | `/api/schedules` | массив Schedule |
| POST | `/api/schedules` | `{ "name", "prompt", "cron"?, "timezone"?, "run_at"? }` → Schedule, 201; ровно один из `cron` и `run_at` |
| PATCH | `/api/schedules/{id}` | `{ "enabled":true/false }` → Schedule |
| DELETE | `/api/schedules/{id}` | 204 |
| GET | `/api/schedules/{id}/runs` | массив Run |

Task: `{id,prompt,status,plan,result,error,created_at,updated_at,llm_calls,events:[{id,kind,message,created_at}]}`. Статусы: `queued`, `planning`, `executing`, `completed`, `failed`, `cancelled`. Event `kind`: `stage`, `llm`, `tool`, `result`, `error`, `info`.

Schedule: `{id,name,prompt,cron,timezone,run_at?,next_run_at?,enabled,status,created_at,last_error?}`. Статусы: `scheduled`, `running`, `paused`, `completed`, `failed`, `interrupted`. Run: `{id,schedule_id,task_id,status,started_at,finished_at?,result,error}`; статусы `running`, `completed`, `failed`, `interrupted`. При запуске расписания создаётся новая Task и выполняется полный агентный цикл. Неисполненные после аварии Run получают `interrupted`, а расписание выключается до проверки человеком. Пропущенные recurring интервалы объединяются в один запуск.
