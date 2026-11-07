import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../api'
import { Badge, Button, Card, CardTitle, ErrorText, Field, OkText, StepBar, Sub } from '../components/ui'

const ROLE_TEXT = { test: '测试开发', backend: '后端开发', ai: 'AI / 算法' }
const STATUS_TEXT = {
  created: '未开始',
  asking: '进行中',
  answering: '进行中',
  scoring: '待评分',
  done: '已完成',
  aborted: '已放弃',
}

export default function Home({ onOpen, onGuard, onLogout }) {
  const [resumes, setResumes] = useState([])
  const [interviews, setInterviews] = useState([])
  const [trend, setTrend] = useState([])
  const [loading, setLoading] = useState(true)

  const [file, setFile] = useState(null)
  const [uploading, setUploading] = useState(false)
  const [uploadErr, setUploadErr] = useState('')
  const [uploadOk, setUploadOk] = useState('')

  const [resumeId, setResumeId] = useState('')
  const [jd, setJd] = useState('')
  const [role, setRole] = useState('test')
  const [turns, setTurns] = useState('5')
  const [creating, setCreating] = useState(false)
  const [createErr, setCreateErr] = useState('')

  const jdRef = useRef(null)

  const load = useCallback(async () => {
    try {
      const [r, i, t] = await Promise.all([
        api('/api/resumes'),
        api('/api/interviews'),
        api('/api/stats/trend?limit=10').catch(() => ({ items: [] })),
      ])
      setResumes(r.items || [])
      setInterviews(i.items || [])
      setTrend(t.items || [])
      setResumeId((prev) => prev || String(r.items?.[0]?.id ?? ''))
    } catch (e) {
      if (!onGuard(e)) setUploadErr(e.message)
    } finally {
      setLoading(false)
    }
  }, [onGuard])

  useEffect(() => {
    load()
  }, [load])

  const upload = async () => {
    if (!file || uploading) return
    setUploadErr('')
    setUploadOk('')
    setUploading(true)
    try {
      const fd = new FormData()
      fd.append('file', file)
      const data = await api('/api/resumes', { method: 'POST', body: fd })
      setUploadOk(
        data.duplicate
          ? `这份简历已经传过了（${data.chars} 字），直接用它`
          : `已解析 ${data.chars} 字，${data.parse_status === 'ok' ? '结构已提取' : '仅存原文'}`,
      )
      setFile(null)
      if (jdRef.current) jdRef.current.value = ''
      await load()
      setResumeId(String(data.id))
    } catch (e) {
      if (!onGuard(e)) setUploadErr(e.message)
    } finally {
      setUploading(false)
    }
  }

  const create = async () => {
    if (creating) return
    setCreateErr('')
    if (!resumeId) return setCreateErr('先上传一份简历')
    if (jd.trim().length < 10) return setCreateErr('岗位描述至少 10 个字')

    setCreating(true)
    try {
      const data = await api('/api/interviews', {
        method: 'POST',
        body: JSON.stringify({
          resume_id: Number(resumeId),
          jd_text: jd.trim(),
          role_type: role,
          max_turns: Number(turns),
        }),
      })
      onOpen(data.id, 'chat')
    } catch (e) {
      if (!onGuard(e)) setCreateErr(e.message)
    } finally {
      setCreating(false)
    }
  }

  const steps = useMemo(() => {
    const hasResume = resumes.length > 0
    return [
      { label: '上传简历', state: hasResume ? 'done' : 'active' },
      { label: '开始面试', state: hasResume ? 'active' : 'todo' },
      { label: '拿报告', state: 'todo' },
    ]
  }, [resumes])

  const done = interviews.filter((i) => i.status === 'done')
  const avg = done.length
    ? Math.round(done.reduce((s, i) => s + (i.report?.total || 0), 0) / done.length)
    : null

  return (
    <>
      <StepBar steps={steps} />

      <Card className="mb-3">
        <CardTitle>上传简历</CardTitle>
        <Sub>支持 PDF 和 Word(.docx)。扫描件或图片读不出字，会被拒绝。</Sub>

        {resumes.length > 0 && (
          <div className="mb-3 space-y-2">
            {resumes.slice(0, 3).map((r) => (
              <div
                key={r.id}
                className="flex items-center gap-3 rounded-lg border border-emerald-500/25 bg-white/[0.03] px-3 py-2"
              >
                <span className="flex h-7 w-7 flex-none items-center justify-center rounded-md bg-emerald-500/15 text-xs text-emerald-400">
                  ✓
                </span>
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[13px] text-slate-200">{r.filename}</div>
                  <div className="text-[11px] text-slate-500">
                    {r.parse_status === 'ok' ? '结构已提取' : '仅存原文'}
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}

        <div className="flex gap-2">
          <input
            type="file"
            accept=".pdf,.docx"
            onChange={(e) => setFile(e.target.files?.[0] || null)}
            className="min-w-0 flex-1 text-xs text-slate-400 file:mr-3 file:rounded-md file:border-0 file:bg-white/[0.06] file:px-3 file:py-1.5 file:text-xs file:text-slate-300"
          />
          <Button variant="primary" onClick={upload} disabled={!file || uploading}>
            {uploading ? '解析中…' : '上传'}
          </Button>
        </div>

        <ErrorText>{uploadErr}</ErrorText>
        <OkText>{uploadOk}</OkText>
      </Card>

      <Card className="mb-3 border-grape/40 shadow-glow">
        <CardTitle badge={<Badge tone="info">第 2 步</Badge>}>岗位描述</CardTitle>
        <Sub>写得越具体，出的题越贴你的经历</Sub>

        <Field label="选简历">
          <select
            className="input-dark"
            value={resumeId}
            onChange={(e) => setResumeId(e.target.value)}
          >
            {resumes.length === 0 && <option value="">（还没有简历，先上传一份）</option>}
            {resumes.map((r) => (
              <option key={r.id} value={r.id}>
                {r.filename}
              </option>
            ))}
          </select>
        </Field>

        <Field label="岗位描述（至少 10 字）">
          <textarea
            ref={jdRef}
            className="input-dark min-h-[84px] resize-y"
            placeholder="例：负责测试开发，需要接口自动化与质量体系建设经验，熟悉 Python / pytest / JMeter。"
            value={jd}
            onChange={(e) => setJd(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
                e.preventDefault()
                create()
              }
            }}
          />
        </Field>

        <div className="mb-3 flex gap-2">
          <Field label="岗位方向" className="flex-1">
            <select className="input-dark" value={role} onChange={(e) => setRole(e.target.value)}>
              <option value="test">测试开发</option>
              <option value="backend">后端开发</option>
              <option value="ai">AI / 算法</option>
            </select>
          </Field>
          <Field label="轮次" className="flex-1">
            <select className="input-dark" value={turns} onChange={(e) => setTurns(e.target.value)}>
              <option value="3">3 轮 · 约 10 分钟</option>
              <option value="5">5 轮 · 约 20 分钟</option>
              <option value="8">8 轮 · 约 30 分钟</option>
            </select>
          </Field>
        </div>

        <Button variant="primary" className="w-full" onClick={create} disabled={creating}>
          {creating ? '创建中…' : '创建并开始'}
        </Button>
        <p className="mt-2 text-center text-[11px] text-slate-600">
          Ctrl + Enter 快速创建
        </p>
        <ErrorText>{createErr}</ErrorText>
      </Card>

      {trend.length >= 2 && (
        <Card className="mb-3">
          <CardTitle>成绩趋势</CardTitle>
          <Sub>最近 {trend.length} 场已完成面试</Sub>
          <TrendChart items={trend} />
        </Card>
      )}

      <Card>
        <div className="mb-1 flex items-baseline gap-2">
          <h2 className="text-[13px] font-medium text-slate-100">历史面试</h2>
          <span className="flex-1" />
          <span className="text-xs text-slate-500">
            {interviews.length} 场{avg !== null && ` · 平均 ${avg}`}
          </span>
        </div>

        {loading && <p className="py-6 text-center text-xs text-slate-600">加载中…</p>}

        {!loading && interviews.length === 0 && (
          <p className="py-6 text-center text-xs text-slate-600">
            还没有面试记录，上面填完岗位描述就能开始
          </p>
        )}

        {interviews.map((iv) => {
          const total = iv.report?.total
          const isDone = iv.status === 'done'
          return (
            <div
              key={iv.id}
              className="flex items-center gap-3 border-t border-white/[0.06] py-2.5 first:border-t-0"
            >
              <span
                className={[
                  'flex h-9 w-9 flex-none items-center justify-center rounded-lg text-[13px] font-medium',
                  isDone && total >= 70 && 'bg-gradient-to-br from-emerald-500/90 to-emerald-600/90 text-white shadow-[0_0_14px_rgba(16,185,129,0.35)]',
                  isDone && total < 70 && 'bg-gradient-to-br from-amber-500/90 to-amber-600/90 text-white shadow-[0_0_14px_rgba(245,158,11,0.3)]',
                  !isDone && 'border border-white/[0.1] bg-white/[0.06] text-[11px] text-slate-400',
                ]
                  .filter(Boolean)
                  .join(' ')}
              >
                {isDone ? total : `${iv.current_seq}/${iv.max_turns}`}
              </span>

              <div className="min-w-0 flex-1">
                <div className="text-[13px] text-slate-200">
                  {ROLE_TEXT[iv.role_type] || iv.role_type} · {iv.max_turns} 轮
                </div>
                <div className="text-[11px] text-slate-500">
                  {fmtTime(iv.created_at)} · {STATUS_TEXT[iv.status] || iv.status}
                </div>
              </div>

              <Button
                variant="link"
                className="text-xs"
                onClick={() => onOpen(iv.id, isDone ? 'report' : 'chat')}
              >
                {isDone ? '看报告' : '继续'}
              </Button>
            </div>
          )
        })}
      </Card>
    </>
  )
}

function fmtTime(iso) {
  if (!iso) return ''
  const d = new Date(iso)
  const now = new Date()
  const sameDay = d.toDateString() === now.toDateString()
  const hm = `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
  if (sameDay) return `今天 ${hm}`
  const days = Math.round((now - d) / 86400000)
  if (days === 1) return `昨天 ${hm}`
  if (days < 7) return `${days} 天前`
  return `${d.getMonth() + 1} 月 ${d.getDate()} 日`
}

// 折线图手写 SVG，不引图表库——只有几条线，加一个依赖不值得。
function TrendChart({ items }) {
  const W = 800
  const H = 110
  const PAD = 8
  const n = items.length
  const stepX = n > 1 ? (W - PAD * 2) / (n - 1) : 0
  const y = (v) => H - PAD - (v / 100) * (H - PAD * 2)
  const x = (i) => PAD + i * stepX

  const path = (vals) => {
    let d = ''
    let started = false
    vals.forEach((v, i) => {
      if (v == null) return
      d += `${started ? ' L' : 'M'}${x(i).toFixed(1)} ${y(v).toFixed(1)}`
      started = true
    })
    return d
  }

  const totals = items.map((it) => it.total || 0)

  return (
    <div>
      <svg viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className="h-[110px] w-full">
        <defs>
          <linearGradient id="trendLine" x1="0" y1="0" x2="1" y2="0">
            <stop offset="0%" stopColor="#8B5CF6" />
            <stop offset="100%" stopColor="#22D3EE" />
          </linearGradient>
        </defs>
        {[0, 50, 100].map((v) => (
          <line
            key={v}
            x1="0"
            y1={y(v)}
            x2={W}
            y2={y(v)}
            stroke="rgba(255,255,255,0.06)"
            strokeWidth="1"
          />
        ))}
        <path d={path(totals)} fill="none" stroke="url(#trendLine)" strokeWidth="2" strokeLinejoin="round" />
        {items.map((it, i) => (
          <circle
            key={it.interview_id}
            cx={x(i)}
            cy={y(it.total || 0)}
            r="3"
            fill="#0B0B14"
            stroke="#A78BFA"
            strokeWidth="2"
          />
        ))}
      </svg>
      <div className="mt-2 text-[11px] text-slate-500">
        最近 {n} 场：{totals[0]} → {totals[n - 1]}
        {totals[n - 1] > totals[0] && (
          <span className="ml-1 text-emerald-400">↑ {totals[n - 1] - totals[0]} 分</span>
        )}
      </div>
    </div>
  )
}
