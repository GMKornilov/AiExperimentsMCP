import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { api, errorMessage, isAbortError, type Config, type Run, type Schedule, type Task, type TaskStatus, type ToolsResponse } from './api'

type Tab = 'tasks' | 'schedules' | 'tools'
type LoadState = 'loading' | 'ready' | 'error'

const statusLabels: Record<TaskStatus, string> = {
  queued: 'В очереди', planning: 'Планирование', executing: 'Выполнение',
  completed: 'Готово', failed: 'Ошибка', cancelled: 'Отменено',
}
const steps: TaskStatus[] = ['queued', 'planning', 'executing', 'completed']
const activeStatuses: TaskStatus[] = ['queued', 'planning', 'executing']
const runLabels: Record<string, string> = { pending: 'Ожидает', running: 'Выполняется', completed: 'Готово', failed: 'Ошибка', interrupted: 'Прервано', cancelled: 'Отменено' }
const serverLabels: Record<string, string> = { connected: 'Подключён', ok: 'Доступен', error: 'Ошибка', disconnected: 'Недоступен' }
const scheduleLabels: Record<string, string> = { scheduled: 'Запланировано', running: 'Выполняется', completed: 'Завершено', failed: 'Ошибка', interrupted: 'Прервано', disabled: 'Выключено' }

function formatDate(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat('ru-RU', { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}

function formatTime(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat('ru-RU', { hour: '2-digit', minute: '2-digit' }).format(date)
}

function Badge({ label, tone = 'neutral' }: { label: string; tone?: string }) {
  return <span className={`badge badge--${tone}`}>{label}</span>
}

function StateMessage({ title, text, action }: { title: string; text: string; action?: React.ReactNode }) {
  return <div className="state-message"><div className="state-symbol" aria-hidden="true">◎</div><h3>{title}</h3><p>{text}</p>{action}</div>
}

function InlineError({ message }: { message: string }) {
  return <div className="inline-error" role="alert"><span aria-hidden="true">!</span>{message}</div>
}

function taskTone(status: TaskStatus): string {
  return status === 'completed' ? 'success' : status === 'failed' ? 'danger' : status === 'cancelled' ? 'neutral' : 'blue'
}

function TaskDetail({ task, onCancel, cancelling }: { task: Task; onCancel: () => void; cancelling: boolean }) {
  const currentIndex = steps.indexOf(task.status)
  return <div className="task-detail">
    <div className="detail-head">
      <div><div className="eyebrow">ЗАДАЧА · {task.id.slice(0, 8)}</div><h2>Ход выполнения</h2><p className="muted detail-prompt">{task.prompt}</p></div>
      <div className="detail-actions"><Badge label={statusLabels[task.status]} tone={taskTone(task.status)} />{activeStatuses.includes(task.status) && <button type="button" className="button button--subtle" onClick={onCancel} disabled={cancelling}>{cancelling ? 'Отмена…' : 'Отменить'}</button>}</div>
    </div>
    <div className="progress-steps" aria-label={`Статус: ${statusLabels[task.status]}`}>
      {steps.map((step, index) => <div key={step} className={`progress-step ${currentIndex >= index ? 'is-done' : ''} ${task.status === step ? 'is-current' : ''}`}><span className="step-dot">{currentIndex > index ? '✓' : index + 1}</span><span>{statusLabels[step]}</span></div>)}
    </div>
    <div className="metrics-row"><span><strong>{task.llm_calls}</strong> вызовов LLM</span><span>Создана {formatDate(task.created_at)}</span><span>Обновлена {formatTime(task.updated_at)}</span></div>
    {(task.plan || task.status === 'planning') && <section className="detail-section"><div className="section-label"><span className="section-icon">▤</span><h3>План</h3></div>{task.plan ? <div className="text-panel pre-wrap">{task.plan}</div> : <p className="muted">Агент составляет план…</p>}</section>}
    {(task.result || task.error || task.status === 'completed' || task.status === 'failed') && <section className="detail-section"><div className="section-label"><span className="section-icon">✦</span><h3>{task.error ? 'Ошибка' : 'Результат'}</h3></div>{task.error ? <InlineError message={task.error} /> : task.result ? <div className="text-panel pre-wrap result-panel">{task.result}</div> : <p className="muted">Результат пока пуст.</p>}</section>}
    <section className="detail-section"><div className="section-label"><span className="section-icon">≋</span><h3>Лента событий</h3><span className="count-pill">{task.events?.length ?? 0}</span></div>
      {task.events?.length ? <ol className="event-list">{[...task.events].reverse().map(event => <li key={event.id} className="event"><span className={`event-mark event-mark--${event.kind}`} aria-hidden="true" /><div className="event-content"><div className="event-meta"><span>{event.kind === 'llm' ? 'LLM' : event.kind === 'tool' ? 'Инструмент' : event.kind === 'error' ? 'Ошибка' : event.kind === 'result' ? 'Результат' : event.kind === 'stage' ? 'Этап' : 'Событие'}</span><time dateTime={event.created_at}>{formatTime(event.created_at)}</time></div><p className="pre-wrap">{event.message}</p></div></li>)}</ol> : <p className="muted">События появятся по мере выполнения.</p>}
    </section>
  </div>
}

function TasksView({ tasks, loadState, loadError, refresh, requestedTaskId, onSelectTask }: { tasks: Task[]; loadState: LoadState; loadError: string; refresh: () => void; requestedTaskId: string | null; onSelectTask: (id: string) => void }) {
  const [prompt, setPrompt] = useState('')
  const [selectedId, setSelectedId] = useState<string | null>(requestedTaskId)
  useEffect(() => { if (requestedTaskId) setSelectedId(requestedTaskId) }, [requestedTaskId])
  const [submitting, setSubmitting] = useState(false)
  const [cancelling, setCancelling] = useState(false)
  const [actionError, setActionError] = useState('')
  const selected = selectedId ? tasks.find(task => task.id === selectedId) : tasks[0]
  const sorted = useMemo(() => [...tasks].sort((a, b) => b.created_at.localeCompare(a.created_at)), [tasks])

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (!prompt.trim() || submitting) return
    setSubmitting(true); setActionError('')
    try {
      const task = await api<Task>('/tasks', { method: 'POST', body: JSON.stringify({ prompt: prompt.trim() }) })
      setPrompt(''); setSelectedId(task.id); onSelectTask(task.id); refresh()
    } catch (error) { setActionError(errorMessage(error)) }
    finally { setSubmitting(false) }
  }

  async function cancel() {
    if (!selected || cancelling) return
    setCancelling(true); setActionError('')
    try { await api<Task>(`/tasks/${encodeURIComponent(selected.id)}/cancel`, { method: 'POST', body: '{}' }); refresh() }
    catch (error) { setActionError(errorMessage(error)) }
    finally { setCancelling(false) }
  }

  return <main className="page-content" id="main-content"><div className="page-heading"><div><div className="eyebrow">РАБОЧЕЕ ПРОСТРАНСТВО</div><h1>Задачи</h1><p>Опишите цель — агент составит план и выполнит её.</p></div><Badge label={`${tasks.length} всего`} /></div>
    <div className="tasks-layout"><div className="tasks-column"><section className="card composer-card"><div className="card-heading"><span className="section-icon section-icon--blue">✳</span><h2>Новая задача</h2></div><form onSubmit={submit}><label htmlFor="task-prompt" className="field-label">Что нужно сделать?</label><textarea id="task-prompt" value={prompt} onChange={event => setPrompt(event.target.value)} placeholder="Например: найди свежие данные и подготовь краткую сводку…" rows={5} required /><div className="composer-footer"><span>Агент сначала покажет план, затем начнёт выполнение.</span><button className="button button--primary" type="submit" disabled={submitting || !prompt.trim()}>{submitting ? 'Запуск…' : 'Запустить задачу'}<span aria-hidden="true">↗</span></button></div></form>{actionError && <InlineError message={actionError} />}</section>
      <section className="card history-card"><div className="card-heading card-heading--spread"><h2>История задач</h2><span className="count-pill">{tasks.length}</span></div>{loadState === 'loading' && !tasks.length ? <div className="skeleton-list" aria-label="Загрузка задач"><div /><div /><div /></div> : loadState === 'error' && !tasks.length ? <StateMessage title="Не удалось загрузить задачи" text={loadError} action={<button className="button button--subtle" onClick={refresh}>Повторить</button>} /> : !tasks.length ? <StateMessage title="Пока нет задач" text="Создайте первую задачу, чтобы увидеть её выполнение здесь." /> : <div className="history-list">{sorted.map(task => <button type="button" key={task.id} className={`history-item ${selected?.id === task.id ? 'is-selected' : ''}`} onClick={() => { setSelectedId(task.id); onSelectTask(task.id) }} aria-pressed={selected?.id === task.id}><span className="history-title">{task.prompt}</span><span className="history-meta"><Badge label={statusLabels[task.status]} tone={taskTone(task.status)} /><time dateTime={task.created_at}>{formatDate(task.created_at)}</time></span></button>)}</div>}{loadState === 'error' && tasks.length > 0 && <InlineError message={loadError} />}</section></div>
      <section className="card detail-card">{selected ? <TaskDetail task={selected} onCancel={cancel} cancelling={cancelling} /> : <StateMessage title={selectedId ? "Загрузка задачи" : "Здесь появится ход задачи"} text={selectedId ? "Обновляем список задач…" : "Выберите задачу из истории или создайте новую."} />}</section></div>
  </main>
}

function SchedulesView({ schedules, config, loadState, loadError, refresh, onOpenTask }: { schedules: Schedule[]; config: Config | null; loadState: LoadState; loadError: string; refresh: () => void; onOpenTask: (id: string) => void }) {
  const [name, setName] = useState('')
  const [prompt, setPrompt] = useState('')
  const [mode, setMode] = useState<'once' | 'cron'>('once')
  const [runAt, setRunAt] = useState('')
  const [cron, setCron] = useState('')
  const [timezone, setTimezone] = useState('')
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [runs, setRuns] = useState<Run[]>([])
  const [runsState, setRunsState] = useState<LoadState>('loading')
  const [runsError, setRunsError] = useState('')
  const [busyId, setBusyId] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [actionError, setActionError] = useState('')
  const [revision, setRevision] = useState(0)
  const selected = schedules.find(schedule => schedule.id === selectedId) ?? schedules[0]

  useEffect(() => { if (config?.timezone) setTimezone(previous => previous || config.timezone) }, [config?.timezone])
  useEffect(() => {
    if (!selected?.id) { setRuns([]); return }
    let mounted = true
    let controller: AbortController | null = null
    const load = async () => {
      controller?.abort(); controller = new AbortController()
      try { const data = await api<Run[]>(`/schedules/${encodeURIComponent(selected.id)}/runs`, { signal: controller.signal }); if (mounted) { setRuns(data); setRunsState('ready'); setRunsError('') } }
      catch (error) { if (mounted && !isAbortError(error)) { setRunsState('error'); setRunsError(errorMessage(error)) } }
    }
    setRunsState('loading'); setRuns([]); void load()
    const timer = window.setInterval(() => void load(), 4000)
    return () => { mounted = false; controller?.abort(); window.clearInterval(timer) }
  }, [selected?.id, revision])

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (submitting) return
    setActionError('')
    if (!prompt.trim()) { setActionError('Опишите задачу для агента.'); return }
    if (mode === 'once' && (!runAt || Number.isNaN(new Date(runAt).getTime()))) { setActionError('Укажите дату и время запуска.'); return }
    if (mode === 'cron' && (!cron.trim() || !timezone.trim())) { setActionError('Укажите cron и часовой пояс.'); return }
    const body = { name: name.trim(), prompt: prompt.trim(), ...(mode === 'once' ? { run_at: new Date(runAt).toISOString() } : { cron: cron.trim(), timezone: timezone.trim() }) }
    setSubmitting(true)
    try { const created = await api<Schedule>('/schedules', { method: 'POST', body: JSON.stringify(body) }); setName(''); setPrompt(''); setRunAt(''); setCron(''); setSelectedId(created.id); refresh(); setRevision(value => value + 1) }
    catch (error) { setActionError(errorMessage(error)) }
    finally { setSubmitting(false) }
  }

  async function toggle(schedule: Schedule) {
    setBusyId(schedule.id); setActionError('')
    try { await api<Schedule>(`/schedules/${encodeURIComponent(schedule.id)}`, { method: 'PATCH', body: JSON.stringify({ enabled: !schedule.enabled }) }); refresh() }
    catch (error) { setActionError(errorMessage(error)) }
    finally { setBusyId(null) }
  }

  async function remove(schedule: Schedule) {
    if (!window.confirm(`Удалить расписание «${schedule.name}»?`)) return
    setBusyId(schedule.id); setActionError('')
    try { await api<void>(`/schedules/${encodeURIComponent(schedule.id)}`, { method: 'DELETE' }); if (selectedId === schedule.id) setSelectedId(null); refresh() }
    catch (error) { setActionError(errorMessage(error)) }
    finally { setBusyId(null) }
  }

  return <main className="page-content" id="main-content"><div className="page-heading"><div><div className="eyebrow">АВТОМАТИЗАЦИЯ</div><h1>Расписания</h1><p>Запускайте агента по времени: с новым планом и результатом каждого запуска.</p></div><Badge label={`${schedules.length} всего`} /></div>
    <div className="schedule-layout"><section className="card schedule-form"><div className="card-heading"><span className="section-icon section-icon--blue">◷</span><h2>Новое расписание</h2></div><form onSubmit={submit} className="form-stack"><div><label htmlFor="schedule-name" className="field-label">Название</label><input id="schedule-name" value={name} onChange={event => setName(event.target.value)} placeholder="Ежедневная сводка" required /></div><div><label htmlFor="schedule-prompt" className="field-label">Что агент должен сделать при запуске?</label><textarea id="schedule-prompt" value={prompt} onChange={event => setPrompt(event.target.value)} placeholder="Например: пришли мне в Telegram все мои встречи сегодня" rows={5} required /><p className="field-hint">При каждом запуске агент составит новый план и выполнит задачу.</p></div><fieldset className="mode-fieldset"><legend className="field-label">Когда запускать</legend><div className="segmented"><label><input type="radio" name="mode" checked={mode === 'once'} onChange={() => setMode('once')} /><span>Один раз</span></label><label><input type="radio" name="mode" checked={mode === 'cron'} onChange={() => setMode('cron')} /><span>По cron</span></label></div></fieldset>{mode === 'once' ? <div><label htmlFor="run-at" className="field-label">Дата и время · локальное</label><input id="run-at" type="datetime-local" value={runAt} onChange={event => setRunAt(event.target.value)} required /></div> : <div className="two-fields"><div><label htmlFor="cron" className="field-label">Cron · 5 полей</label><input id="cron" value={cron} onChange={event => setCron(event.target.value)} placeholder="0 9 * * 1-5" required /><p className="field-hint">Минуты, часы, день, месяц, день недели</p></div><div><label htmlFor="timezone" className="field-label">Часовой пояс · IANA</label><input id="timezone" value={timezone} onChange={event => setTimezone(event.target.value)} placeholder="Europe/Moscow" required /></div></div>}<button type="submit" className="button button--primary button--full" disabled={submitting || !prompt.trim()}>{submitting ? 'Создание…' : 'Создать расписание'}<span aria-hidden="true">↗</span></button>{actionError && <InlineError message={actionError} />}</form></section>
    <div className="schedule-main"><section className="card schedule-list-card"><div className="card-heading card-heading--spread"><h2>Все расписания</h2><span className="count-pill">{schedules.length}</span></div>{loadState === 'loading' && !schedules.length ? <div className="skeleton-list" aria-label="Загрузка расписаний"><div /><div /></div> : loadState === 'error' && !schedules.length ? <StateMessage title="Не удалось загрузить расписания" text={loadError} action={<button className="button button--subtle" onClick={refresh}>Повторить</button>} /> : !schedules.length ? <StateMessage title="Расписаний пока нет" text="Создайте первое расписание для задачи агента." /> : <div className="schedule-list">{[...schedules].sort((a, b) => b.created_at.localeCompare(a.created_at)).map(schedule => <article key={schedule.id} className={`schedule-item ${selected?.id === schedule.id ? 'is-selected' : ''}`}><div className="schedule-item-top"><button type="button" className="schedule-select" onClick={() => setSelectedId(schedule.id)} aria-pressed={selected?.id === schedule.id}><strong>{schedule.name}</strong><span>{schedule.prompt}</span></button><Badge label={scheduleLabels[schedule.status] ?? (schedule.enabled ? 'Активно' : 'Выключено')} tone={schedule.status === 'failed' || schedule.status === 'interrupted' ? 'danger' : schedule.enabled ? 'success' : 'neutral'} /></div><div className="schedule-data"><span>{schedule.cron ? `${schedule.cron} · ${schedule.timezone}` : `Один раз · ${formatDate(schedule.run_at)}`}</span><span>Следующий запуск: {formatDate(schedule.next_run_at)}</span>{schedule.last_error && <span className="danger-text">{schedule.last_error}</span>}{schedule.run_at && schedule.status === 'completed' && <span>Разовый запуск завершён; повторное включение недоступно.</span>}</div><div className="schedule-controls"><label className="switch-label"><input type="checkbox" checked={schedule.enabled} onChange={() => void toggle(schedule)} disabled={busyId === schedule.id || schedule.status === 'running' || (!!schedule.run_at && schedule.status === 'completed')} title={schedule.status === 'running' ? 'Дождитесь завершения запуска' : schedule.run_at && schedule.status === 'completed' ? 'Разовый запуск уже завершён' : undefined} /><span className="switch-ui" aria-hidden="true" />{schedule.enabled ? 'Включено' : 'Выключено'}</label><button type="button" className="text-button danger-text" onClick={() => void remove(schedule)} disabled={busyId === schedule.id || schedule.status === 'running'} title={schedule.status === 'running' ? 'Дождитесь завершения запуска' : undefined}>Удалить</button></div></article>)}</div>}{loadState === 'error' && schedules.length > 0 && <InlineError message={loadError} />}</section>
    <section className="card runs-card"><div className="card-heading card-heading--spread"><h2>История запусков</h2>{selected && <span className="muted small-text">{selected.name}</span>}</div>{!selected ? <StateMessage title="Выберите расписание" text="Здесь появятся его запуски и результаты." /> : runsState === 'loading' ? <div className="skeleton-list" aria-label="Загрузка запусков"><div /><div /></div> : runsState === 'error' ? <StateMessage title="Не удалось загрузить запуски" text={runsError} action={<button className="button button--subtle" onClick={() => setRevision(value => value + 1)}>Повторить</button>} /> : !runs.length ? <StateMessage title="Запусков пока нет" text="Когда наступит время, результат появится здесь." /> : <div className="runs-list">{[...runs].sort((a, b) => b.started_at.localeCompare(a.started_at)).map(run => <article className="run-item" key={run.id}><div className="run-head"><Badge label={runLabels[run.status] ?? run.status} tone={run.status === 'completed' ? 'success' : run.status === 'failed' || run.status === 'interrupted' ? 'danger' : 'blue'} /><time dateTime={run.started_at}>{formatDate(run.started_at)}</time></div>{run.result && <p className="pre-wrap">{run.result}</p>}{run.task_id && <button type="button" className="text-button task-link" onClick={() => onOpenTask(run.task_id!)}>Открыть задачу ↗</button>}{run.error && <InlineError message={run.error} />}{run.finished_at && <span className="muted small-text">Завершён {formatDate(run.finished_at)}</span>}</article>)}</div>}</section></div></div>
  </main>
}

function ToolsView({ tools, config, loadState, loadError, refresh }: { tools: ToolsResponse | null; config: Config | null; loadState: LoadState; loadError: string; refresh: () => void }) {
  return <main className="page-content" id="main-content"><div className="page-heading"><div><div className="eyebrow">ИНТЕГРАЦИИ</div><h1>Инструменты</h1><p>Подключённые MCP серверы и доступные действия.</p></div><button type="button" className="button button--subtle" onClick={refresh}>Обновить ↻</button></div>
    {loadState === 'loading' && !tools ? <div className="card"><div className="skeleton-list" aria-label="Загрузка инструментов"><div /><div /><div /></div></div> : loadState === 'error' && !tools ? <div className="card"><StateMessage title="Не удалось загрузить инструменты" text={loadError} action={<button className="button button--subtle" onClick={refresh}>Повторить</button>} /></div> : <><div className="section-heading"><h2>Серверы</h2><span className="count-pill">{tools?.servers.length ?? 0}</span></div>{tools?.servers.length ? <div className="server-grid">{tools.servers.map(server => <div className="card server-card" key={server.name}><div className="server-icon">⌘</div><div className="server-info"><strong>{server.name}</strong><span>{config?.mcp_servers.find(item => item.name === server.name)?.url ?? 'MCP сервер'}</span>{server.error && <span className="danger-text">{server.error}</span>}</div><Badge label={serverLabels[server.status] ?? server.status} tone={server.status === 'connected' || server.status === 'ok' ? 'success' : 'danger'} /></div>)}</div> : <div className="card"><StateMessage title="Серверы не настроены" text="Добавьте MCP серверы в конфигурацию сервиса." /></div>}
    <div className="section-heading section-heading--spaced"><h2>Доступные инструменты</h2><span className="count-pill">{tools?.tools.length ?? 0}</span></div>{tools?.tools.length ? <div className="tool-grid">{tools.tools.map(tool => <article className="card tool-card" key={`${tool.server}/${tool.name}`}><div className="tool-card-top"><div className="tool-icon">{tool.name.slice(0, 1).toUpperCase()}</div><Badge label={tool.server} /></div><h3>{tool.name}</h3><p>{tool.description || 'Описание отсутствует.'}</p><details className="schema-hint"><summary>Входная JSON-схема</summary><pre>{JSON.stringify(tool.input_schema ?? {}, null, 2)}</pre></details></article>)}</div> : <div className="card"><StateMessage title="Инструменты не найдены" text="Проверьте доступность серверов и обновите список." /></div>}</>}{loadState === 'error' && tools && <InlineError message={loadError} />}
  </main>
}

export default function App() {
  const [tab, setTab] = useState<Tab>('tasks')
  const [requestedTaskId, setRequestedTaskId] = useState<string | null>(null)
  const [config, setConfig] = useState<Config | null>(null)
  const [configError, setConfigError] = useState('')
  const [tools, setTools] = useState<ToolsResponse | null>(null)
  const [toolsState, setToolsState] = useState<LoadState>('loading')
  const [toolsError, setToolsError] = useState('')
  const [tasks, setTasks] = useState<Task[]>([])
  const [tasksState, setTasksState] = useState<LoadState>('loading')
  const [tasksError, setTasksError] = useState('')
  const [schedules, setSchedules] = useState<Schedule[]>([])
  const [schedulesState, setSchedulesState] = useState<LoadState>('loading')
  const [schedulesError, setSchedulesError] = useState('')
  const [tasksRevision, setTasksRevision] = useState(0)
  const [schedulesRevision, setSchedulesRevision] = useState(0)
  const [toolsRevision, setToolsRevision] = useState(0)

  useEffect(() => {
    let mounted = true; let controller: AbortController | null = null
    const load = async () => { controller?.abort(); controller = new AbortController(); try { const value = await api<Config>('/config', { signal: controller.signal }); if (mounted) { setConfig(value); setConfigError('') } } catch (error) { if (mounted && !isAbortError(error)) setConfigError(errorMessage(error)) } }
    void load(); const timer = window.setInterval(() => void load(), 30000)
    return () => { mounted = false; controller?.abort(); window.clearInterval(timer) }
  }, [])
  useEffect(() => {
    let mounted = true; let controller: AbortController | null = null
    const load = async () => { controller?.abort(); controller = new AbortController(); try { const value = await api<ToolsResponse>('/tools', { signal: controller.signal }); if (mounted) { setTools(value); setToolsState('ready'); setToolsError('') } } catch (error) { if (mounted && !isAbortError(error)) { setToolsState('error'); setToolsError(errorMessage(error)) } } }
    void load(); const timer = window.setInterval(() => void load(), 15000)
    return () => { mounted = false; controller?.abort(); window.clearInterval(timer) }
  }, [toolsRevision])
  useEffect(() => {
    let mounted = true; let controller: AbortController | null = null
    const load = async () => { controller?.abort(); controller = new AbortController(); try { const value = await api<Task[]>('/tasks', { signal: controller.signal }); if (mounted) { setTasks(value); setTasksState('ready'); setTasksError('') } } catch (error) { if (mounted && !isAbortError(error)) { setTasksState('error'); setTasksError(errorMessage(error)) } } }
    void load(); const timer = window.setInterval(() => void load(), 2000)
    return () => { mounted = false; controller?.abort(); window.clearInterval(timer) }
  }, [tasksRevision])
  useEffect(() => {
    let mounted = true; let controller: AbortController | null = null
    const load = async () => { controller?.abort(); controller = new AbortController(); try { const value = await api<Schedule[]>('/schedules', { signal: controller.signal }); if (mounted) { setSchedules(value); setSchedulesState('ready'); setSchedulesError('') } } catch (error) { if (mounted && !isAbortError(error)) { setSchedulesState('error'); setSchedulesError(errorMessage(error)) } } }
    void load(); const timer = window.setInterval(() => void load(), 5000)
    return () => { mounted = false; controller?.abort(); window.clearInterval(timer) }
  }, [schedulesRevision])

  return <div className="app-shell"><aside className="sidebar"><div className="brand"><div className="brand-mark">✳</div><div><strong>Agent Loop</strong><span>Локальная панель</span></div></div><nav className="nav" aria-label="Основная навигация"><button type="button" className={`nav-item ${tab === 'tasks' ? 'is-active' : ''}`} onClick={() => setTab('tasks')} aria-current={tab === 'tasks' ? 'page' : undefined}><span aria-hidden="true">▤</span>Задачи</button><button type="button" className={`nav-item ${tab === 'schedules' ? 'is-active' : ''}`} onClick={() => setTab('schedules')} aria-current={tab === 'schedules' ? 'page' : undefined}><span aria-hidden="true">◷</span>Расписания</button><button type="button" className={`nav-item ${tab === 'tools' ? 'is-active' : ''}`} onClick={() => setTab('tools')} aria-current={tab === 'tools' ? 'page' : undefined}><span aria-hidden="true">⌘</span>Инструменты</button></nav><div className="sidebar-bottom"><div className="model-card"><div className="model-card-top"><span className="model-dot" /><span>КОНФИГУРАЦИЯ МОДЕЛИ</span></div>{config ? <><strong>{config.model}</strong><div className="model-meta"><span>{config.provider}</span>{config.provider.toLowerCase() === 'demo' && <Badge label="DEMO" tone="blue" />}</div><div className="model-settings"><span>Температура <b>{config.temperature}</b></span><span>Итерации <b>{config.max_iterations}</b></span></div></> : <span className="model-loading">{configError || 'Загрузка…'}</span>}</div><span className="sidebar-footnote">Локальный агент · {config?.timezone ?? '—'}</span></div></aside>
    <div className="mobile-topbar"><div className="brand-mark">✳</div><strong>Agent Loop</strong><span className="mobile-model">{config?.model ?? 'Локальный агент'}</span></div><div className="mobile-nav" role="navigation" aria-label="Основная навигация"><button type="button" className={tab === 'tasks' ? 'is-active' : ''} onClick={() => setTab('tasks')}>Задачи</button><button type="button" className={tab === 'schedules' ? 'is-active' : ''} onClick={() => setTab('schedules')}>Расписания</button><button type="button" className={tab === 'tools' ? 'is-active' : ''} onClick={() => setTab('tools')}>Инструменты</button></div>{config && <div className="mobile-config"><span>{config.provider}</span><span>Температура {config.temperature}</span><span>Итерации {config.max_iterations}</span>{config.provider.toLowerCase() === 'demo' && <Badge label="DEMO" tone="blue" />}</div>}
    {tab === 'tasks' ? <TasksView tasks={tasks} loadState={tasksState} loadError={tasksError} refresh={() => setTasksRevision(value => value + 1)} requestedTaskId={requestedTaskId} onSelectTask={setRequestedTaskId} /> : tab === 'schedules' ? <SchedulesView schedules={schedules} config={config} loadState={schedulesState} loadError={schedulesError} refresh={() => setSchedulesRevision(value => value + 1)} onOpenTask={id => { setRequestedTaskId(id); setTab('tasks'); setTasksRevision(value => value + 1) }} /> : <ToolsView tools={tools} config={config} loadState={toolsState} loadError={toolsError} refresh={() => setToolsRevision(value => value + 1)} />}
  </div>
}
