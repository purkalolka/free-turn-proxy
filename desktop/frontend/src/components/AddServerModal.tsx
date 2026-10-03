import {useEffect, useState} from 'react'
import {backend, ImportLink, NewServer, ParseLink, SaveServer} from '../api'
import CreateServerWizard from './CreateServerWizard'

interface Props {
  onClose: () => void
  onCreated: (s: backend.Server) => void
}

export default function AddServerModal({onClose, onCreated}: Props) {
  const [tab, setTab] = useState<'link' | 'manual' | 'deploy'>('link')
  const [name, setName] = useState('')
  const [vkLink, setVkLink] = useState('')
  const [peer, setPeer] = useState('')
  const [linkText, setLinkText] = useState('')
  const [preview, setPreview] = useState<backend.ShareLink | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    setPreview(null)
    setError('')
    if (tab !== 'link' || linkText.trim() === '') return
    const t = setTimeout(() => {
      ParseLink(linkText.trim())
        .then(setPreview)
        .catch((e) => setError(String(e)))
    }, 250)
    return () => clearTimeout(t)
  }, [linkText, tab])

  const submitLink = async () => {
    setBusy(true)
    setError('')
    try {
      const base = await NewServer()
      base.name = name
      if (vkLink.trim()) base.vkLinks = [vkLink.trim()]
      const saved = await ImportLink(linkText.trim(), base)
      onCreated(saved)
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(false)
    }
  }

  const submitManual = async () => {
    setBusy(true)
    setError('')
    try {
      const base = await NewServer()
      base.name = name
      base.peer = peer.trim()
      if (vkLink.trim()) base.vkLinks = [vkLink.trim()]
      const saved = await SaveServer(base)
      onCreated(saved)
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h2>Добавить сервер</h2>
        <div className="tabs">
          <div className={'tab' + (tab === 'link' ? ' active' : '')} onClick={() => setTab('link')}>
            Вставить ссылку
          </div>
          <div className={'tab' + (tab === 'manual' ? ' active' : '')} onClick={() => setTab('manual')}>
            Вручную
          </div>
          <div className={'tab' + (tab === 'deploy' ? ' active' : '')} onClick={() => setTab('deploy')}>
            Новый VPS
          </div>
        </div>

        {tab === 'deploy' && <CreateServerWizard onCreated={onCreated} />}

        {tab !== 'deploy' && error && <div className="notice error">{error}</div>}

        {tab !== 'deploy' && (
          <div className="field">
            <label>Имя</label>
            <input type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="Мой сервер" />
          </div>
        )}

        {tab === 'link' && (
          <>
            <div className="field">
              <label>freeturn:// ссылка от владельца сервера</label>
              <textarea value={linkText} onChange={(e) => setLinkText(e.target.value)} placeholder="freeturn://..." />
            </div>
            <div className="field">
              <label>Ваша ссылка на звонок VK Calls</label>
              <input type="text" value={vkLink} onChange={(e) => setVkLink(e.target.value)} placeholder="https://vk.ru/call/join/..." />
            </div>
            {preview && (
              <div className="notice info">
                peer {preview.peer}, обфускация {preview.obfProfile || 'none'}
                {preview.name ? `, имя владельца: ${preview.name}` : ''}
                <br />
                {preview.wgConf
                  ? 'В ссылке есть WireGuard-конфиг — после импорта подключение в один клик (встроенный VPN).'
                  : 'WireGuard-конфига в ссылке нет — будет режим реле (нужен отдельный WireGuard/AmneziaWG-клиент).'}
              </div>
            )}
            <div className="modal-actions">
              <button className="btn" onClick={onClose}>
                Отмена
              </button>
              <button className="btn primary" disabled={!preview || busy} onClick={submitLink}>
                Импортировать
              </button>
            </div>
          </>
        )}
        {tab === 'manual' && (
          <>
            <div className="field">
              <label>Адрес сервера (peer), host:port</label>
              <input type="text" value={peer} onChange={(e) => setPeer(e.target.value)} placeholder="1.2.3.4:56000" />
            </div>
            <div className="field">
              <label>Ссылка на звонок VK Calls</label>
              <input type="text" value={vkLink} onChange={(e) => setVkLink(e.target.value)} placeholder="https://vk.ru/call/join/..." />
            </div>
            <div className="modal-actions">
              <button className="btn" onClick={onClose}>
                Отмена
              </button>
              <button className="btn primary" disabled={!peer.trim() || busy} onClick={submitManual}>
                Создать
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  )
}
