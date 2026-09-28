# Проверка Docker Compose

27 сентября 2026 года, Docker Compose 2.40.3. Проверка выполнялась в отдельном проекте `scheduled-agent-compose-check` на `127.0.0.1:18080`; использовался тестовый env-файл без секретов. Другие Docker-проекты и локальный demo не затрагивались.

| Проверка | Результат |
| --- | --- |
| `docker compose config -q` | Успешно для demo и `config.docker.example.yaml`. |
| `docker compose build` | После исправления конфликта тегов успешно собраны обе multi-stage image, включая Go и frontend. Первоначальная сборка падала из-за записи двух сервисов в один image tag; теперь теги разные. |
| `up -d --wait` | Агент прошёл healthcheck, demo MCP запустился. Готовность MCP подтверждена обнаружением `echo`/`get_time` и успешным вызовом `get_time` через агента. |
| HTTP и frontend | `/api/health` вернул 200 и `status: ok`; `/` и JS asset вернули 200. `/api/tools` обнаружил через сеть Compose работающий demo MCP с `echo` и `get_time`. |
| Demo задача | Задача с `get_time` завершилась со статусом `completed`, непустыми `plan` и `result`. |
| One-shot расписание | Создано с полным текстом задачи, `run_at` примерно через 4 секунды и `timezone: Europe/Moscow`; Run завершился, связанная `task_id` указывает на завершённую задачу с планом и результатом `get_time`. |
| Сохранение SQLite | После `down` без `-v` и повторного `up --wait` сохранились обе завершённые задачи, расписание и завершённый Run. |
| Runtime и образ | Процесс работает под UID `10001`; присутствуют SQLite-файл, frontend и `tzdata` с `Europe/Moscow`. В конфигурации обоих images нет переменных API key или MCP token. |
| Example-конфигурация | `config.docker.example.yaml` с заведомо фиктивным `DEEPSEEK_API_KEY` поднялась до healthy; `/api/config` показал `openai-compatible`, `deepseek-chat`, `Europe/Moscow` и не раскрыл значение ключа. |

Реальный LLM API не вызывался. Визуальный рендеринг интерфейса в браузере и внешние MCP-серверы в этой Docker-проверке не тестировались. После проверки выполнен `down` только проекта `scheduled-agent-compose-check`; его именованный том `scheduled-agent-compose-check_agent-data` оставлен для сохранения тестовых данных.
