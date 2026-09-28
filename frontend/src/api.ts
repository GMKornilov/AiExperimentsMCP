export type TaskStatus = 'queued' | 'planning' | 'executing' | 'completed' | 'failed' | 'cancelled'
export type EventKind = 'stage' | 'llm' | 'tool' | 'result' | 'error' | 'info'

export interface TaskEvent {
  id: string
  kind: EventKind
  message: string
  created_at: string
}

export interface Task {
  id: string
  prompt: string
  status: TaskStatus
  plan: string
  result: string
  error: string
  created_at: string
  updated_at: string
  llm_calls: number
  events: TaskEvent[]
}

export interface Config {
  model: string
  provider: string
  temperature: number
  max_iterations: number
  timezone: string
  mcp_servers: { name: string; url: string }[]
}

export interface Tool {
  name: string
  server: string
  description: string
  input_schema: unknown
}

export interface ToolsResponse {
  servers: { name: string; status: string; error?: string }[]
  tools: Tool[]
}

export interface Schedule {
  id: string
  name: string
  prompt: string
  cron: string
  timezone: string
  run_at?: string
  next_run_at?: string
  enabled: boolean
  status: string
  created_at: string
  last_error?: string
}

export interface Run {
  id: string
  schedule_id: string
  task_id?: string
  status: string
  started_at: string
  finished_at?: string
  result: string
  error: string
}

export class ApiError extends Error {
  constructor(message: string, readonly status: number) {
    super(message)
  }
}

export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch(`/api${path}`, {
    ...options,
    headers: options.body ? { 'Content-Type': 'application/json', ...options.headers } : options.headers,
  })
  if (!response.ok) {
    let message = `Ошибка HTTP ${response.status}`
    try {
      const data = await response.json() as { error?: string }
      if (data.error) message = data.error
    } catch { /* The server may return a non-JSON error. */ }
    throw new ApiError(message, response.status)
  }
  return response.status === 204 ? undefined as T : response.json() as Promise<T>
}

export function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === 'AbortError'
}

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : 'Неизвестная ошибка'
}
