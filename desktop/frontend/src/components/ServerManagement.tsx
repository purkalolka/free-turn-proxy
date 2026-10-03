import {useState} from 'react'
import {backend, ServerLogs, ServerRemoteStatus, ServerRestart, ServerStop, ServerUninstall, serversetup} from '../api'

interface Props {
  server: backend.Server
}

export default function ServerManagement({server}: Props) {
  const [status, setStatus] = useState<serversetup.ProbeResult | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [info, setInfo] = useState('')
  const [logs, setLogs] = useState<string[] | null>(null)

  const run = async (fn: () => Promise<void>) => {
    setBusy(true)
    setError('')
    setInfo('')
    try {
      await fn()
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(false)
    }
  }

  const refresh = () => run(async () => setStatus(await ServerRemoteStatus(server.id)))
  const restart = () => run(async () => {
    await ServerRestart(server.id)
    setStatus(await ServerRemoteStatus(server.id))
    setInfo('Перезапущено на сервере.')
  })
  const stop = () => run(async () => {
    await ServerStop(server.id)
    setStatus(await ServerRemoteStatus(server.id))
    setInfo('Остановлено.')
  })
  const showLogs = () => run(async () => setLogs(await ServerLogs(server.id, 200)))
  const uninstall = () => run(async () => {
    if (!window.confirm('Удалить free-turn-proxy и управляемый WireGuard-интерфейс с этого VPS? Профиль в приложении останется.')) return
    const res = await ServerUninstall(server.id, false)
    setStatus(null)
    setInfo(
      `Удалено: бинарь=${res.removed.binary ? 'да' : 'нет'}, служба=${res.removed.unit ? 'да' : 'нет'}, WireGuard=${res.removed.wg_iface ? 'да' : 'нет'}` +
        (res.kept.length ? `. Не тронуто: ${res.kept.join(', ')}` : '')
    )
  })

  const stateLabel = status ? (status.running ? 'Сервер работает' : status.installed ? 'Установлен, остановлен' : 'Не установлен') : 'Статус неизвестен'
  const stateDot = status?.running ? ' connected' : status && !status.installed ? '' : status ? ' captcha' : ''

  return (
    <div className="field-group">
      <h3>Управление сервером (по SSH)</h3>
      {error && <div className="notice error">{error}</div>}
      {info && <div className="notice info">{info}</div>}
      <div className="checkbox-row">
        <span className={'dot' + stateDot} />
        {stateLabel}
        <button className="btn small ghost" disabled={busy} onClick={refresh} style={{marginLeft: 'auto'}}>
          Обновить статус
        </button>
      </div>
      <div className="inline-actions" style={{marginTop: 8, marginBottom: 8, flexWrap: 'wrap'}}>
        <button className="btn small" disabled={busy} onClick={restart}>
          Перезапустить на сервере
        </button>
        <button className="btn small" disabled={busy} onClick={stop}>
          Остановить
        </button>
        <button className="btn small" disabled={busy} onClick={showLogs}>
          {logs ? 'Обновить логи' : 'Показать логи сервера'}
        </button>
      </div>
      {logs && (
        <pre
          className="mono"
          style={{maxHeight: 220, overflow: 'auto', background: 'var(--bg-input)', padding: 8, borderRadius: 7, border: '1px solid var(--border)', whiteSpace: 'pre-wrap'}}
        >
          {logs.join('\n') || '(пусто)'}
        </pre>
      )}
      {server.wgClientConf && (
        <div className="field" style={{marginTop: 12}}>
          <label>Конфиг WireGuard/AmneziaWG для отдельного клиента</label>
          <textarea readOnly value={server.wgClientConf} onClick={(e) => (e.target as HTMLTextAreaElement).select()} />
        </div>
      )}
      <div className="inline-actions" style={{marginTop: 12}}>
        <button className="btn small danger" disabled={busy} onClick={uninstall}>
          Удалить free-turn-proxy с сервера
        </button>
      </div>
    </div>
  )
}
