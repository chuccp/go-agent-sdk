import { useEffect, useState } from 'react'
import type { FlowInfo } from '../api/flow'
import { listFlows } from '../api/flow'

interface Props {
  onBack: () => void
}

type MenuKey = 'flow'

const MENU_ITEMS: { key: MenuKey; label: string; icon: string }[] = [
  { key: 'flow', label: 'Flow 管理', icon: '🔀' },
]

export function Settings({ onBack }: Props) {
  const [menu, setMenu] = useState<MenuKey>('flow')

  return (
    <div className="settings-layout">
      {/* ── Left Menu ── */}
      <aside className="settings-menu">
        <div className="settings-menu-header">
          <button className="settings-back-btn" onClick={onBack}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
              <polyline points="15,18 9,12 15,6" />
            </svg>
            Back
          </button>
          <h2 className="settings-title">Settings</h2>
        </div>
        <nav className="settings-menu-list">
          {MENU_ITEMS.map((item) => (
            <button
              key={item.key}
              className={`settings-menu-item${menu === item.key ? ' active' : ''}`}
              onClick={() => setMenu(item.key)}
            >
              <span className="settings-menu-icon">{item.icon}</span>
              {item.label}
            </button>
          ))}
        </nav>
      </aside>

      {/* ── Right Panel ── */}
      <main className="settings-panel">
        {menu === 'flow' && <FlowPanel />}
      </main>
    </div>
  )
}

// ── Flow Management Panel ──

function FlowPanel() {
  const [flows, setFlows] = useState<FlowInfo[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    listFlows()
      .then(setFlows)
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
      .finally(() => setLoading(false))
  }, [])

  return (
    <div className="settings-body">
      <section className="settings-section">
        <h3 className="settings-section-title">Flow 管理</h3>
        <p className="settings-section-desc">Agent 中所有已注册的 workflow。</p>

        {loading ? (
          <div className="settings-empty">Loading…</div>
        ) : error ? (
          <div className="settings-empty">Failed to load flows: {error}</div>
        ) : flows.length === 0 ? (
          <div className="settings-empty">No flows registered</div>
        ) : (
          <table className="flow-table">
            <thead>
              <tr>
                <th>ID</th>
                <th>Name</th>
              </tr>
            </thead>
            <tbody>
              {flows.map((f) => (
                <tr key={f.id}>
                  <td className="flow-id">{f.id}</td>
                  <td>{f.name}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </div>
  )
}
