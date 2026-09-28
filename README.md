# Локальный агент с MCP и расписаниями

Агент получает план отдельным запросом к LLM, затем выполняет задачу через MCP инструменты. Расписание сохраняет текст задания и в нужное время запускает новый полный проход агента. Состояние хранится в SQLite.

## Docker Compose

Нужны Docker и Docker Compose 2.24 или новее. Без `.env` запускается demo-конфигурация; ключи не требуются:

```sh
docker compose up --build -d
docker compose logs -f agent
# открыть http://127.0.0.1:8080
docker compose down
```

Порт хоста можно сменить: `AGENT_PORT=18080 docker compose up --build -d`. API доступен только с этого компьютера; demo MCP доступен агенту по имени `demo-mcp:8081` внутри Compose и не публикуется на хост. SQLite хранится в именованном томе `agent-data` и сохраняется после `docker compose down`.

Для реального провайдера один раз скопируйте шаблоны и заполните ключи и настройки MCP:

```sh
cp config.docker.example.yaml config.docker.local.yaml
cp .env.example .env
# отредактировать config.docker.local.yaml и .env
docker compose up --build -d
curl -sS http://127.0.0.1:8080/api/config
docker compose logs -f agent
```

Строка `AGENT_CONFIG=./config.docker.local.yaml` в `.env` выбирает реальный YAML для последующих `docker compose up`. В ответе `GET /api/config` проверьте `provider`, `model` и имена `mcp_servers`: так можно убедиться, что загружена нужная конфигурация, без вывода ключей. Для разового demo запуска при существующем `.env` задайте `AGENT_CONFIG=./config.docker.demo.yaml docker compose up --build -d`; при следующем обычном запуске снова будет выбран локальный YAML.

`docker compose logs -f agent` показывает JSON-события вызовов `llm_call` и `mcp_call`. Начало (`status=started`) и результат (`status=ok` или `error`) связаны общим `call_id`; записи содержат `task_id`, этап (`stage`), длительность, `request`/`response` либо `error`. Размер каждого payload ограничен 8 КиБ, обрезка отмечается полями `request_truncated` и `response_truncated`. Ключи и учётные данные в URL и заголовках маскируются. При запуске события `agent_config` и `demo_provider_active` показывают выбранные провайдер, модель, имена MCP серверов и предупреждают о demo.

Compose монтирует YAML только для чтения в `/app/config.yaml`, а `.env` передаёт сервису `agent` при запуске. `${DEEPSEEK_API_KEY}` в YAML остаётся текстом до загрузки конфигурации приложением. `docker compose --env-file FILE` задаёт переменные для подстановки в Compose, но сам по себе не передаёт произвольные ключи приложению; для другого файла с секретами укажите `AGENT_ENV_FILE=./FILE`. Если MCP запущен на хосте, укажите `http://host.docker.internal:PORT/mcp` в YAML (на Linux Compose использует `host-gateway`). Для другого сервиса Compose используйте его DNS-имя.

Если MCP сервер запускается через supergateway, включите Streamable HTTP на том же пути, что указан в YAML:

```sh
supergateway --stdio '<команда MCP-сервера>' --outputTransport streamableHttp --streamableHttpPath /mcp --port 9000
```

## Быстрый запуск demo

Нужен Go 1.25 и Node.js для интерфейса. В отдельных терминалах из корня проекта:

```sh
go run ./cmd/demo-mcp --listen 127.0.0.1:8081
go run ./cmd/agent --config config.demo.yaml
```

Demo LLM детерминирован и предназначен для проверки подключения; он не разбирает произвольные формулировки времени. Demo MCP предоставляет `echo` и `get_time`. API слушает `127.0.0.1:8080`. Для интерфейса:

```sh
cd frontend
npm ci
npm run dev
```

Адрес Vite выводится в терминале. Для выдачи интерфейса самим Go сервисом выполните `npm run build` в `frontend`; сервис использует `frontend/dist`, если каталог существует.

## Конфигурация реального LLM

Скопируйте `config.example.yaml`, настройте OpenAI-совместимый endpoint и MCP Streamable HTTP сервер. Секреты держите в окружении или `.env`:

```sh
cp config.example.yaml config.yaml
go run ./cmd/agent --config config.yaml --env-file .env
```

Схема YAML:

```yaml
listen: 127.0.0.1:8080
database: data/agent.db
timezone: Europe/Moscow
task_timeout: 5m
llm:
  provider: openai-compatible
  service: deepseek
  model: deepseek-chat
  base_url: https://api.deepseek.com
  api_key: ${DEEPSEEK_API_KEY}
  temperature: 0
  max_iterations: 8
mcp_servers:
  - name: demo
    url: http://127.0.0.1:8081/mcp
    headers: {}
```

`llm.service` — описательная метка сервиса. По умолчанию: `listen=127.0.0.1:8080`, `database=data/agent.db`, `timezone=UTC`, `task_timeout=5m`, `temperature=1`, `max_iterations=8`. Явное `temperature: 0` сохраняется. `${NAME}` поддерживается в `llm.api_key`, URL и заголовках MCP; отсутствие переменной — ошибка запуска. Переменные процесса имеют приоритет над `--env-file`.

## API

Создать задачу:

```sh
curl -sS -X POST http://127.0.0.1:8080/api/tasks \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"Попроси demo echo повторить привет"}'
```

Посмотреть статус: `GET /api/tasks/{id}`. Запланировать однократную задачу:

```sh
curl -sS -X POST http://127.0.0.1:8080/api/schedules \
  -H 'Content-Type: application/json' \
  -d '{"name":"Напоминание","prompt":"Передай через demo echo: Привет","run_at":"2026-10-01T12:00:00+03:00"}'
```

С реальным LLM можно отправить обычную задачу, например «через 15 минут пришли мне встречи», «завтра в 9:00 подготовь сводку» или «каждый будний день в 18:30 проверь отчёт». Агент преобразует время в `run_at` или cron с настроенной зоной. Для исполнения действий нужны соответствующие MCP инструменты; формулировку будущей задачи агент сохраняет в расписании.

Повторяющийся запуск задаётся вместо `run_at` полями `"cron":"0 9 * * 1-5"` и `"timezone":"Europe/Moscow"`. Cron состоит из пяти полей. Список расписаний: `GET /api/schedules`; запуски: `GET /api/schedules/{id}/runs`; пауза: `PATCH /api/schedules/{id}` с `{"enabled":false}`; удаление: `DELETE /api/schedules/{id}`. Полный контракт — [docs/api.md](docs/api.md).

## Как работает расписание

Сервис должен работать, чтобы запуск случился вовремя. При наступлении срока создаётся новая задача: агент заново обнаруживает MCP инструменты, строит план отдельным LLM запросом и выполняет его. Для одной SQLite базы запускайте только один процесс агента. После рестарта просроченный `run_at` выполняется; для cron пропущенные моменты объединяются в один запуск, затем выбирается следующий будущий момент. Одно расписание не запускается параллельно с собой. Если процесс остановился во время выполнения задачи, исход внешнего вызова может быть неизвестен: запуск отмечается `interrupted`, расписание выключается до решения человека. Гарантии exactly-once нет.

Это локальный сервис для доверенной машины; API не предоставляет многопользовательскую аутентификацию. Поддерживается MCP Streamable HTTP. [MCP Tasks](https://tasks.extensions.modelcontextprotocol.io/specification/draft/tasks) описывает асинхронное получение результата MCP задачи, а локальные cron расписания реализованы этим приложением.
