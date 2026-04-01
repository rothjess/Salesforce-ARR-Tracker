import { useState, useEffect, useCallback } from 'react'
import {
  BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip,
  ResponsiveContainer, Cell,
} from 'recharts'

const API = import.meta.env.VITE_API_BASE || ''

const fmt = (n) =>
  n >= 1_000_000
    ? `$${(n / 1_000_000).toFixed(2)}M`
    : n >= 1_000
    ? `$${(n / 1_000).toFixed(1)}K`
    : `$${n.toFixed(0)}`

export default function App() {
  const [summary, setSummary]     = useState(null)
  const [contracts, setContracts] = useState([])
  const [loading, setLoading]     = useState(true)
  const [syncing, setSyncing]     = useState(false)
  const [syncMsg, setSyncMsg]     = useState('')
  const [search, setSearch]       = useState('')
  const [sortKey, setSortKey]     = useState('arr')
  const [sortDir, setSortDir]     = useState('desc')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [s, c] = await Promise.all([
        fetch(`${API}/api/summary`).then(r => r.json()),
        fetch(`${API}/api/contracts?stage=ALL`).then(r => r.json()),
      ])
      setSummary(s)
      setContracts(c || [])
    } catch (e) {
      console.error(e)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { load() }, [load])

  const doSync = async (full = false) => {
    setSyncing(true)
    setSyncMsg('')
    try {
      const r = await fetch(`${API}/api/sync${full ? '?full=true' : ''}`, { method: 'POST' })
      const d = await r.json()
      setSyncMsg(`✓ ${full ? 'Full' : 'Incremental'} sync: ${d.upserted} records upserted`)
      await load()
    } catch (e) {
      setSyncMsg(`✗ Sync failed: ${e.message}`)
    } finally {
      setSyncing(false)
    }
  }

  // Sort + filter
  const filtered = (contracts || [])
    .filter(c =>
      !search ||
      c.DealName?.toLowerCase().includes(search.toLowerCase()) ||
      c.AccountName?.toLowerCase().includes(search.toLowerCase())
    )
    .sort((a, b) => {
      let av = a[sortKey] ?? 0, bv = b[sortKey] ?? 0
      if (typeof av === 'string') av = av.toLowerCase()
      if (typeof bv === 'string') bv = bv.toLowerCase()
      return sortDir === 'asc' ? (av > bv ? 1 : -1) : (av < bv ? 1 : -1)
    })

  const toggleSort = (key) => {
    if (sortKey === key) setSortDir(d => d === 'asc' ? 'desc' : 'asc')
    else { setSortKey(key); setSortDir('desc') }
  }

  // Top 10 chart
  const chartData = (contracts || [])
    .filter(c => c.StageName === 'Closed Won' && c.ARR > 0)
    .sort((a, b) => b.ARR - a.ARR)
    .slice(0, 10)
    .map(c => ({ name: c.AccountName || c.DealName, arr: c.ARR }))

  if (loading) return (
    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100vh', color: 'var(--muted)' }}>
      Loading…
    </div>
  )

  return (
    <div style={{ maxWidth: 1200, margin: '0 auto', padding: '24px 20px' }}>

      {/* Header */}
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 32 }}>
        <div>
          <h1 style={{ fontSize: 22, fontWeight: 700, color: 'var(--text)' }}>ARR Tracker</h1>
          <p style={{ color: 'var(--muted)', fontSize: 12, marginTop: 2 }}>
            Salesforce · {summary?.LastSyncAt ? `Last synced ${new Date(summary.LastSyncAt).toLocaleString()}` : 'Never synced'}
          </p>
        </div>
        <div style={{ display: 'flex', gap: 10, alignItems: 'center' }}>
          {syncMsg && <span style={{ fontSize: 12, color: syncMsg.startsWith('✓') ? 'var(--green)' : 'var(--red)' }}>{syncMsg}</span>}
          <button onClick={() => doSync(false)} disabled={syncing} style={btnStyle('var(--accent)')}>
            {syncing ? '…' : 'Sync Now'}
          </button>
          <button onClick={() => doSync(true)} disabled={syncing} style={btnStyle('var(--border)')}>
            Full Sync
          </button>
        </div>
      </div>

      {/* Summary Cards */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 16, marginBottom: 32 }}>
        <Card label="Total ARR" value={fmt(summary?.TotalARR || 0)} />
        <Card label="MRR" value={fmt(summary?.TotalMRR || 0)} />
        <Card label="Net Delta ARR" value={fmt(summary?.TotalDeltaARR || 0)} accent={summary?.TotalDeltaARR >= 0 ? 'var(--green)' : 'var(--red)'} />
        <Card label="Contracts" value={(summary?.ContractCount || 0).toLocaleString()} />
      </div>

      {/* Chart */}
      {chartData.length > 0 && (
        <div style={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 10, padding: '20px 16px', marginBottom: 28 }}>
          <p style={{ fontWeight: 600, marginBottom: 16, fontSize: 13 }}>Top 10 Accounts by ARR</p>
          <ResponsiveContainer width="100%" height={200}>
            <BarChart data={chartData} margin={{ top: 0, right: 10, bottom: 40, left: 0 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" />
              <XAxis dataKey="name" tick={{ fill: 'var(--muted)', fontSize: 11 }} angle={-35} textAnchor="end" interval={0} />
              <YAxis tickFormatter={fmt} tick={{ fill: 'var(--muted)', fontSize: 11 }} width={70} />
              <Tooltip formatter={(v) => fmt(v)} contentStyle={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 6 }} />
              <Bar dataKey="arr" radius={[4, 4, 0, 0]}>
                {chartData.map((_, i) => (
                  <Cell key={i} fill={`hsl(${230 + i * 8}, 70%, ${60 - i * 2}%)`} />
                ))}
              </Bar>
            </BarChart>
          </ResponsiveContainer>
        </div>
      )}

      {/* Contracts Table */}
      <div style={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 10, overflow: 'hidden' }}>
        <div style={{ padding: '14px 16px', borderBottom: '1px solid var(--border)', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <span style={{ fontWeight: 600, fontSize: 13 }}>Contracts ({filtered.length})</span>
          <input
            placeholder="Search account or deal…"
            value={search}
            onChange={e => setSearch(e.target.value)}
            style={searchStyle}
          />
        </div>
        <div style={{ overflowX: 'auto' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse' }}>
            <thead>
              <tr style={{ borderBottom: '1px solid var(--border)' }}>
                {[
                  { key: 'AccountName', label: 'Account' },
                  { key: 'DealName', label: 'Deal' },
                  { key: 'StageName', label: 'Stage' },
                  { key: 'CloseDate', label: 'Close Date' },
                  { key: 'ARR', label: 'ARR' },
                  { key: 'DeltaARR', label: 'Delta ARR' },
                  { key: 'CurrencyCode', label: 'Currency' },
                ].map(col => (
                  <th
                    key={col.key}
                    onClick={() => toggleSort(col.key)}
                    style={thStyle(col.key === sortKey)}
                  >
                    {col.label}
                    {col.key === sortKey && (sortDir === 'asc' ? ' ↑' : ' ↓')}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {filtered.map((c, i) => (
                <tr key={c.SalesforceID} style={{ borderBottom: '1px solid var(--border)', background: i % 2 === 0 ? 'transparent' : 'rgba(255,255,255,0.02)' }}>
                  <td style={tdStyle}>{c.AccountName || '—'}</td>
                  <td style={{ ...tdStyle, maxWidth: 200, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{c.DealName}</td>
                  <td style={tdStyle}><StageBadge stage={c.StageName} /></td>
                  <td style={{ ...tdStyle, color: 'var(--muted)' }}>{c.CloseDate ? new Date(c.CloseDate).toLocaleDateString() : '—'}</td>
                  <td style={{ ...tdStyle, fontWeight: 600 }}>{fmt(c.ARR || 0)}</td>
                  <td style={{ ...tdStyle, color: (c.DeltaARR || 0) >= 0 ? 'var(--green)' : 'var(--red)' }}>
                    {c.DeltaARR !== 0 ? fmt(c.DeltaARR || 0) : '—'}
                  </td>
                  <td style={{ ...tdStyle, color: 'var(--muted)' }}>{c.CurrencyCode || 'USD'}</td>
                </tr>
              ))}
              {filtered.length === 0 && (
                <tr><td colSpan={7} style={{ ...tdStyle, textAlign: 'center', color: 'var(--muted)', padding: '40px' }}>No contracts found</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  )
}

function Card({ label, value, accent }) {
  return (
    <div style={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 10, padding: '16px 18px' }}>
      <p style={{ color: 'var(--muted)', fontSize: 11, fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.05em', marginBottom: 6 }}>{label}</p>
      <p style={{ fontSize: 26, fontWeight: 700, color: accent || 'var(--text)' }}>{value}</p>
    </div>
  )
}

function StageBadge({ stage }) {
  const color = stage === 'Closed Won' ? 'var(--green)' : stage === 'Closed Lost' ? 'var(--red)' : 'var(--yellow)'
  return (
    <span style={{ background: color + '22', color, borderRadius: 4, padding: '2px 8px', fontSize: 11, fontWeight: 600 }}>
      {stage}
    </span>
  )
}

const btnStyle = (bg) => ({
  background: bg,
  color: 'var(--text)',
  border: '1px solid var(--border)',
  borderRadius: 6,
  padding: '7px 14px',
  fontSize: 12,
  fontWeight: 600,
  cursor: 'pointer',
})

const searchStyle = {
  background: 'var(--bg)',
  border: '1px solid var(--border)',
  borderRadius: 6,
  padding: '6px 12px',
  color: 'var(--text)',
  fontSize: 12,
  width: 240,
  outline: 'none',
}

const thStyle = (active) => ({
  padding: '10px 14px',
  textAlign: 'left',
  fontSize: 11,
  fontWeight: 700,
  color: active ? 'var(--text)' : 'var(--muted)',
  textTransform: 'uppercase',
  letterSpacing: '0.05em',
  cursor: 'pointer',
  userSelect: 'none',
  whiteSpace: 'nowrap',
})

const tdStyle = {
  padding: '10px 14px',
  fontSize: 13,
}
