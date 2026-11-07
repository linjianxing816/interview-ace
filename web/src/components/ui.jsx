// 所有页面的视觉基础件。
//
// 为什么集中在这里：galaxy 风格靠「渐变 + 玻璃 + 光晕」三种手法反复用，
// 散落在各页面里改一次要改十几处，集中定义才能保持一致。

export function Button({ variant = 'ghost', className = '', ...props }) {
  const base =
    'inline-flex items-center justify-center gap-1.5 rounded-lg px-4 py-2 text-[13px] font-medium transition disabled:cursor-not-allowed disabled:opacity-50'

  const styles = {
    primary: 'bg-grad-main text-white shadow-glow hover:brightness-110',
    ghost:
      'border border-white/[0.1] bg-white/[0.05] text-slate-300 hover:bg-white/[0.08] hover:text-slate-100',
    link: 'px-0 py-0 text-grape hover:text-grape/80',
  }

  return <button className={`${base} ${styles[variant]} ${className}`} {...props} />
}

export function Card({ className = '', children }) {
  return <div className={`glass p-4 ${className}`}>{children}</div>
}

export function CardTitle({ children, badge, className = '' }) {
  return (
    <div className={`mb-1 flex items-center gap-2 ${className}`}>
      <h2 className="text-[13px] font-medium text-slate-100">{children}</h2>
      {badge}
    </div>
  )
}

export function Sub({ children, className = '' }) {
  return <p className={`mb-3 text-xs text-slate-500 ${className}`}>{children}</p>
}

const TONES = {
  ok: 'bg-emerald-500/15 text-emerald-400',
  warn: 'bg-amber-500/15 text-amber-400',
  info: 'bg-blue-500/15 text-blue-400',
  danger: 'bg-red-500/15 text-red-400',
  neutral: 'bg-white/[0.06] text-slate-400',
}

export function Badge({ tone = 'neutral', children, className = '' }) {
  return (
    <span
      className={`inline-block whitespace-nowrap rounded-full px-2 py-[1px] text-[11px] ${TONES[tone]} ${className}`}
    >
      {children}
    </span>
  )
}

export function Field({ label, hint, children, className = '' }) {
  return (
    <div className={`mb-3 last:mb-0 ${className}`}>
      {label && <label className="mb-1.5 block text-xs text-slate-500">{label}</label>}
      {children}
      {hint && <p className="mt-1 text-[11px] text-slate-600">{hint}</p>}
    </div>
  )
}

export function ErrorText({ children }) {
  if (!children) return null
  return <p className="mt-2 text-xs text-red-400">{children}</p>
}

export function OkText({ children }) {
  if (!children) return null
  return <p className="mt-2 text-xs text-emerald-400">{children}</p>
}

// StepBar 三步流程条。
// 状态由调用方算好传进来：done 打勾、active 高亮发光、todo 灰掉。
//
// 布局要点（改之前先看这段，踩过）：
//   最后一步不能设 flex:1 —— `flex: 1` 简写会把 flex-basis 也设成 0%，
//   最后一项本来是自然宽度，被设成 0 之后只剩 flex-grow 撑着，
//   文字会被挤成一列竖排（「拿报告」变成竖着的三个字）。
//   正确做法：只有中间那些步设 flex:1（让连接线能伸缩），最后一步不设 flex。
//   序号圆和文字都加 flex-none + whitespace-nowrap，否则窄屏时圆会被压成椭圆。
export function StepBar({ steps }) {
  return (
    <div className="mb-4 flex items-center">
      {steps.map((s, i) => {
        const last = i === steps.length - 1
        return (
          <div
            key={s.label}
            className="flex items-center"
            // 非最后一步吃掉剩余空间，最后一步保持自然宽度
            style={last ? undefined : { flex: 1, minWidth: 0 }}
          >
            <div className="flex flex-none items-center gap-2">
              <span
                className={[
                  'flex h-5 w-5 flex-none items-center justify-center rounded-full text-[10px]',
                  s.state === 'done' && 'bg-emerald-500 text-white',
                  s.state === 'active' && 'bg-grad-main text-white shadow-glow-sm',
                  s.state === 'todo' && 'border border-white/[0.15] text-slate-600',
                ]
                  .filter(Boolean)
                  .join(' ')}
              >
                {s.state === 'done' ? '✓' : i + 1}
              </span>
              <span
                className={[
                  'whitespace-nowrap text-xs',
                  s.state === 'active' && 'font-medium text-slate-100',
                  s.state === 'done' && 'text-slate-400',
                  s.state === 'todo' && 'text-slate-600',
                ]
                  .filter(Boolean)
                  .join(' ')}
              >
                {s.label}
              </span>
            </div>

            {!last && (
              <span
                className="mx-2 h-px min-w-4 flex-1 rounded"
                style={{
                  background:
                    s.state === 'done'
                      ? 'linear-gradient(90deg, #10B981, #8B5CF6)'
                      : 'rgba(255,255,255,0.08)',
                }}
              />
            )}
          </div>
        )
      })}
    </div>
  )
}

// ProgressBar 渐变流光进度条，用在面试轮次上。
export function ProgressBar({ value }) {
  return (
    <div className="mb-4 h-1 overflow-hidden rounded-full bg-white/[0.07]">
      <div
        className="h-full rounded-full bg-grad-bar shadow-glow-cyan transition-[width] duration-300"
        style={{ width: `${Math.min(100, Math.max(0, value))}%` }}
      />
    </div>
  )
}
