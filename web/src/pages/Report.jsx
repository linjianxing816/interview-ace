import { useCallback, useEffect, useState } from 'react'
import { api } from '../api'
import { Badge, Button, Card, ErrorText } from '../components/ui'

const ROLE_TEXT = { test: '测试开发', backend: '后端开发', ai: 'AI / 算法' }
const DIM_TEXT = { accuracy: '准确性', clarity: '清晰度', depth: '深度' }
const LEVEL_TONE = { 优秀: 'ok', 良好: 'ok', 一般: 'warn', 待提升: 'danger' }

export default function Report({ id, onBack, onGuard }) {
  const [iv, setIv] = useState(null)
  const [err, setErr] = useState('')

  const load = useCallback(async () => {
    try {
      setIv(await api(`/api/interviews/${id}`))
    } catch (e) {
      if (!onGuard(e)) setErr(e.message)
    }
  }, [id, onGuard])

  useEffect(() => {
    load()
  }, [load])

  if (err) {
    return (
      <Card>
        <p className="py-8 text-center text-xs text-red-400">{err}</p>
      </Card>
    )
  }

  if (!iv) {
    return (
      <Card>
        <p className="py-8 text-center text-xs text-slate-600">加载中…</p>
      </Card>
    )
  }

  // report 落库时是 jsonb，接口返回可能已经是对象，也可能是字符串
  const r = typeof iv.report === 'string' ? safeParse(iv.report) : iv.report

  if (!r) {
    return (
      <>
        <Card className="mb-3">
          <p className="py-8 text-center text-xs text-slate-500">
            这场面试还没有报告{iv.status === 'scoring' && '，去对话页点「生成报告」'}
          </p>
        </Card>
        <Button onClick={onBack}>返回首页</Button>
      </>
    )
  }

  return (
    <>
      <Card className="mb-3">
        <div className="flex items-center gap-5 border-b border-white/[0.07] pb-4">
          <ScoreRing total={r.total} />

          <div className="min-w-0 flex-1">
            <div className="mb-1 flex flex-wrap items-center gap-2">
              <Badge tone={LEVEL_TONE[r.level] || 'neutral'}>{r.level}</Badge>
              <span className="text-[13px] font-medium text-slate-100">
                {ROLE_TEXT[iv.role_type] || iv.role_type} · {iv.max_turns} 轮
              </span>
            </div>
            <div className="mb-2 text-xs text-slate-500">
              {fmtDate(iv.created_at)} · 总分 = 三维度平均分 ÷ 5 × 100
            </div>
            <SampleNote report={r} />
          </div>
        </div>

        <div className="mt-4 space-y-3">
          {(r.dimensions || []).map((d) => (
            <Dimension key={d.dimension} dim={d} />
          ))}
        </div>
      </Card>

      <div className="grid gap-2.5 sm:grid-cols-3">
        <Col title="做得好" tone="ok" items={r.strengths} />
        <Col title="需要补" tone="warn" items={r.weaknesses} />
        <Col title="改进建议" tone="info" items={r.suggestions} />
      </div>

      {(r.followup_stats || []).length > 0 && (
        <Card className="mt-3">
          <div className="mb-3 flex flex-wrap items-baseline justify-between gap-1">
            <h3 className="text-sm font-semibold text-slate-100">追问归因</h3>
            <span className="text-[11px] text-slate-500">按追问类型算平均分，最弱的排前面</span>
          </div>
          <div className="space-y-2.5">
            {r.followup_stats.map((s, i) => (
              <FollowupRow key={s.kind} stat={s} weakest={i === 0} />
            ))}
          </div>
        </Card>
      )}

      <TurnReview turns={iv.turns} turnScores={r.turn_scores} />

      <div className="mt-3 flex gap-2">
        <Button variant="primary" className="flex-1" onClick={onBack}>
          再来一场
        </Button>
        <Button className="flex-1" onClick={onBack}>
          返回首页
        </Button>
      </div>
    </>
  )
}

// ScoreRing 环形分数。弧长按分数真实计算（2πr × 比例），不是画个好看的弧。
function ScoreRing({ total }) {
  const R = 44
  const C = 2 * Math.PI * R
  const offset = C * (1 - Math.min(100, Math.max(0, total)) / 100)

  return (
    <div className="relative h-[104px] w-[104px] flex-none">
      <svg width="104" height="104" viewBox="0 0 104 104">
        <defs>
          <linearGradient id="ringGrad" x1="0" y1="0" x2="1" y2="1">
            <stop offset="0%" stopColor="#8B5CF6" />
            <stop offset="50%" stopColor="#3B82F6" />
            <stop offset="100%" stopColor="#22D3EE" />
          </linearGradient>
        </defs>
        <circle cx="52" cy="52" r={R} stroke="rgba(255,255,255,0.07)" strokeWidth="7" fill="none" />
        <circle
          cx="52"
          cy="52"
          r={R}
          stroke="url(#ringGrad)"
          strokeWidth="7"
          fill="none"
          strokeDasharray={C}
          strokeDashoffset={offset}
          strokeLinecap="round"
          transform="rotate(-90 52 52)"
          style={{ filter: 'drop-shadow(0 0 6px rgba(139,92,246,0.45))' }}
        />
      </svg>
      <div className="absolute inset-0 flex flex-col items-center justify-center">
        <span className="text-[26px] leading-none text-slate-50">{total}</span>
        <span className="mt-0.5 text-[10px] text-slate-600">/ 100</span>
      </div>
    </div>
  )
}

function Dimension({ dim }) {
  const pct = (dim.score / 5) * 100
  // 分数决定颜色：4 分以上紫蓝、3 分琥珀、2 分以下红。
  // 扫一眼就知道哪项弱，不用读数字。
  const grad =
    dim.score >= 4
      ? 'linear-gradient(90deg,#8B5CF6,#60A5FA)'
      : dim.score >= 3
        ? 'linear-gradient(90deg,#F59E0B,#FBBF24)'
        : 'linear-gradient(90deg,#EF4444,#F87171)'
  const glow =
    dim.score >= 4
      ? 'rgba(139,92,246,0.45)'
      : dim.score >= 3
        ? 'rgba(245,158,11,0.4)'
        : 'rgba(239,68,68,0.4)'

  return (
    <div>
      <div className="mb-1.5 flex items-baseline justify-between">
        <span className="text-xs text-slate-300">{DIM_TEXT[dim.dimension] || dim.dimension}</span>
        <span className="text-[11px] text-slate-500">{dim.score} / 5</span>
      </div>
      <div className="h-1.5 overflow-hidden rounded-full bg-white/[0.07]">
        <div
          className="h-full rounded-full"
          style={{ width: `${pct}%`, background: grad, boxShadow: `0 0 10px ${glow}` }}
        />
      </div>
      {dim.comment && (
        <p className="mt-1.5 text-[11px] leading-relaxed text-slate-500">{dim.comment}</p>
      )}
    </div>
  )
}

// FollowupRow 是一行追问归因。
//
// 条长按 5 分制折算（avg/5），不是按「所有类型里的最高分」折算——
// 后者在「全都只有 2 分」时也会画出一条满格，看着像没问题。
function FollowupRow({ stat, weakest }) {
  const avg = Number(stat.avg_score) || 0
  const pct = Math.max(0, Math.min(100, (avg / 5) * 100))
  const grad =
    avg >= 3.5
      ? 'linear-gradient(90deg,#8B5CF6,#60A5FA)'
      : avg >= 2.5
        ? 'linear-gradient(90deg,#F59E0B,#FBBF24)'
        : 'linear-gradient(90deg,#EF4444,#F87171)'

  return (
    <div className="flex items-center gap-3">
      <span className="w-12 flex-none text-xs text-slate-300">{stat.label}</span>
      <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-white/[0.07]">
        <div className="h-full rounded-full" style={{ width: `${pct}%`, background: grad }} />
      </div>
      <span className="w-[70px] flex-none text-right text-[11px] tabular-nums text-slate-500">
        {stat.count} 轮 · {avg.toFixed(1)}
      </span>
      {weakest && <Badge tone="danger">最弱</Badge>}
    </div>
  )
}

// TurnReview 逐题回顾：把每轮的分数摊开，按得分从低到高排。
//
// 数据来自两处：题目在 iv.turns（详情接口返回），分数在报告的 turn_scores。
// 报告里不存题目是有意的——报告是评分的快照，题目文本另存一份会不一致。
//
// 排序用升序：用户打开报告最想知道的是「哪题拖了后腿」，
// 按原顺序排的话得自己一题题找低分。
function TurnReview({ turns, turnScores }) {
  const questionOf = new Map((turns || []).map((t) => [t.seq, t.question]))

  const rows = (turnScores || [])
    .map((ts) => {
      const scores = ts.scores || []
      const avg = scores.length
        ? scores.reduce((sum, s) => sum + s.score, 0) / scores.length
        : 0
      // 最弱维度：分数最低的那个，用来提示「这题差在哪」
      const weakest = scores.reduce(
        (min, s) => (s.score < min.score ? s : min),
        scores[0] || { dimension: '', score: 0 },
      )
      return { seq: ts.seq, avg, weakest, question: questionOf.get(ts.seq) || `第 ${ts.seq} 题` }
    })
    .sort((a, b) => a.avg - b.avg)

  if (rows.length === 0) return null

  return (
    <Card className="mt-3">
      <div className="mb-3 flex flex-wrap items-baseline justify-between gap-1">
        <h3 className="text-sm font-semibold text-slate-100">逐题回顾</h3>
        <span className="text-[11px] text-slate-500">按得分从低到高，先看最该补的</span>
      </div>
      <div className="space-y-1.5">
        {rows.map((row) => (
          <div
            key={row.seq}
            className="flex items-center gap-3 rounded-lg bg-white/[0.02] px-3 py-2"
          >
            <span className="w-11 flex-none text-[11px] text-slate-500">第 {row.seq} 题</span>
            <span className="min-w-0 flex-1 truncate text-xs text-slate-300" title={row.question}>
              {row.question}
            </span>
            <span className="w-10 flex-none text-right text-[11px] text-slate-500">
              {DIM_TEXT[row.weakest.dimension] || '—'}
            </span>
            <span
              className={[
                'w-11 flex-none rounded-md py-0.5 text-center text-[11px] font-medium tabular-nums',
                row.avg < 2.5
                  ? 'bg-red-500/15 text-red-300'
                  : row.avg < 3.5
                    ? 'bg-amber-500/15 text-amber-300'
                    : 'bg-emerald-500/15 text-emerald-300',
              ].join(' ')}
            >
              {row.avg.toFixed(1)}
            </span>
          </div>
        ))}
      </div>
    </Card>
  )
}

function SampleNote({ report }) {
  const n = report.sample_count || 1
  const failed = report.sample_failed || 0

  return (
    <div className="flex flex-wrap items-center gap-2">
      <Badge tone={n > 1 && report.consistent ? 'ok' : n > 1 ? 'warn' : 'neutral'}>
        {n > 1 ? `${n} 次采样` : '单次采样'}
      </Badge>
      {failed > 0 && <Badge tone="danger">{failed} 次失败</Badge>}
      <span className="text-[11px] text-slate-500">
        {n > 1 && report.consistent
          ? '三个维度分差都在 1 分内，结果稳定'
          : n > 1
            ? `${(report.unstable_dims || []).map((d) => DIM_TEXT[d] || d).join('、')} 波动较大，仅供参考`
            : '只跑了一次，分数可能有抖动'}
      </span>
    </div>
  )
}

function Col({ title, tone, items }) {
  const border = {
    ok: 'border-emerald-500/20 bg-emerald-500/[0.06]',
    warn: 'border-amber-500/20 bg-amber-500/[0.06]',
    info: 'border-blue-500/20 bg-blue-500/[0.06]',
  }[tone]

  const list = items || []

  return (
    <div className={`rounded-xl border p-3 ${border}`}>
      <Badge tone={tone} className="mb-2">
        {title}
      </Badge>
      {list.length === 0 ? (
        <p className="text-[11px] text-slate-600">（无）</p>
      ) : (
        <ul className="space-y-1.5">
          {list.map((s, i) => (
            <li key={i} className="text-[11px] leading-relaxed text-slate-400">
              {s}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function safeParse(s) {
  try {
    return JSON.parse(s)
  } catch {
    return null
  }
}

function fmtDate(iso) {
  if (!iso) return ''
  const d = new Date(iso)
  return `${d.getMonth() + 1} 月 ${d.getDate()} 日 ${String(d.getHours()).padStart(2, '0')}:${String(
    d.getMinutes(),
  ).padStart(2, '0')}`
}
