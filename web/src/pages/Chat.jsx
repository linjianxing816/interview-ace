import { useCallback, useEffect, useState } from 'react'
import { api } from '../api'
import { Badge, Button, Card, ErrorText, ProgressBar } from '../components/ui'

const ROLE_TEXT = { test: '测试开发', backend: '后端开发', ai: 'AI / 算法' }

// 回答少于这个字数时提示「太短」。
//
// 只提示、不拦提交：真实面试里「不知道」也是有效回答，
// 拦下来反而不真实——而且用户可能就是不想答了。
// 提示的作用是让人意识到「这样写大概率会被扣分」，不是替人做决定。
const MIN_ANSWER_HINT = 15

const KIND_TEXT = {
  cause: '追问 · 归因',
  probe: '追问 · 递进',
  value: '追问 · 价值',
  blindspot: '追问 · 盲区',
  verify: '追问 · 验证',
  boundary: '追问 · 边界',
  tradeoff: '追问 · 权衡',
}

export default function Chat({ id, onBack, onReport, onGuard }) {
  const [iv, setIv] = useState(null)
  const [answer, setAnswer] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const load = useCallback(async () => {
    try {
      const data = await api(`/api/interviews/${id}`)
      setIv(data)
    } catch (e) {
      if (!onGuard(e)) setErr(e.message)
    }
  }, [id, onGuard])

  useEffect(() => {
    load()
  }, [load])

  // 每次操作后重新拉详情，UI 完全由后端 status 驱动。
  // 本地自己拼状态容易和后端不一致（比如出题失败回滚），重新拉最省心。
  const act = async (fn) => {
    if (busy) return
    setErr('')
    setBusy(true)
    try {
      await fn()
      await load()
    } catch (e) {
      if (!onGuard(e)) setErr(e.message)
    } finally {
      setBusy(false)
    }
  }

  const start = () => act(() => api(`/api/interviews/${id}/start`, { method: 'POST' }))

  const submit = () =>
    act(async () => {
      await api(`/api/interviews/${id}/answer`, {
        method: 'POST',
        body: JSON.stringify({ answer: answer.trim() }),
      })
      setAnswer('')
    })

  const score = () =>
    act(async () => {
      await api(`/api/interviews/${id}/score`, { method: 'POST' })
      onReport()
    })

  if (!iv) {
    return (
      <Card>
        <p className="py-8 text-center text-xs text-slate-600">{err || '加载中…'}</p>
      </Card>
    )
  }

  const turns = iv.turns || []
  const canAnswer = iv.status === 'asking' || iv.status === 'answering'

  return (
    <Card>
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <h2 className="text-[13px] font-medium text-slate-100">面试进行中</h2>
        <Badge tone="info">
          第 {Math.max(1, iv.current_seq)} / {iv.max_turns} 轮
        </Badge>
        <span className="flex-1" />
        <span className="text-xs text-slate-500">
          {ROLE_TEXT[iv.role_type] || iv.role_type}
        </span>
      </div>

      <ProgressBar value={(turns.filter((t) => t.answered).length / iv.max_turns) * 100} />

      {turns.length === 0 && iv.status === 'created' && (
        <div className="py-6 text-center">
          <p className="mb-4 text-xs text-slate-500">简历和岗位描述都准备好了，开始吧</p>
          <Button variant="primary" onClick={start} disabled={busy}>
            {busy ? '出题中…' : '开始面试'}
          </Button>
        </div>
      )}

      <div className="space-y-2.5">
        {turns.map((t) => (
          <div key={t.id}>
            <div className="rounded-lg border border-grape/20 border-l-[3px] border-l-grape bg-grape/[0.07] px-3.5 py-3">
              <div className="mb-1.5 flex gap-1.5">
                <Badge tone="neutral">第 {t.seq} 题</Badge>
                {t.question_type === 'followup' && (
                  <Badge tone="info">{KIND_TEXT[t.followup_kind] || '追问'}</Badge>
                )}
              </div>
              <p className="whitespace-pre-wrap text-[13px] leading-[1.75] text-slate-200">
                {t.question}
              </p>
            </div>

            {t.answered && (
              <div className="mt-2 rounded-lg border border-white/[0.07] border-l-[3px] border-l-white/[0.18] bg-white/[0.025] px-3.5 py-2.5">
                <div className="mb-1 text-[11px] text-slate-600">你的回答</div>
                <p className="whitespace-pre-wrap text-xs leading-[1.7] text-slate-400">
                  {t.answer}
                </p>
              </div>
            )}
          </div>
        ))}
      </div>

      {canAnswer && (
        <div className="mt-4">
          <label className="mb-1.5 block text-xs text-slate-500">
            你的回答 —— 说清「怎么做的、为什么这么做、遇到什么问题」
          </label>
          <textarea
            className="input-dark min-h-[96px] resize-y"
            placeholder="例：我用唯一索引加事务兜底，因为先查后插存在 TOCTOU 窗口…"
            value={answer}
            onChange={(e) => setAnswer(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault()
                submit()
              }
            }}
          />

          <div className="mt-2 flex flex-wrap items-center gap-2">
            <Button variant="primary" onClick={submit} disabled={busy || !answer.trim()}>
              {busy ? '出下一题…' : '提交回答'}
            </Button>
            <Button onClick={onBack}>先离开</Button>
            <span className="flex-1" />
            <span className="text-[11px] text-slate-600">
              Enter 提交 · Shift+Enter 换行
            </span>
            <span
              className={[
                'text-[11px] tabular-nums',
                answer.length > 0 && answer.length < MIN_ANSWER_HINT
                  ? 'text-amber-400'
                  : 'text-slate-600',
              ].join(' ')}
            >
              {answer.length} 字
            </span>
          </div>

          {answer.length > 0 && answer.length < MIN_ANSWER_HINT && (
            <p className="mt-1.5 text-[11px] leading-relaxed text-amber-400/80">
              回答偏短。说清「怎么做的、为什么这么做、结果如何」更容易得分，也可以直接提交。
            </p>
          )}
        </div>
      )}

      {iv.status === 'scoring' && (
        <div className="mt-5 text-center">
          <p className="mb-3 text-xs text-slate-500">
            面试已结束，AI 会评三个维度，跑 3 次取中位数
          </p>
          <Button variant="primary" onClick={score} disabled={busy}>
            {busy ? '评分中…（约 10 秒）' : '生成报告'}
          </Button>
        </div>
      )}

      {iv.status === 'done' && (
        <div className="mt-5 text-center">
          <Button variant="primary" onClick={onReport}>
            看报告
          </Button>
        </div>
      )}

      <ErrorText>{err}</ErrorText>
    </Card>
  )
}
