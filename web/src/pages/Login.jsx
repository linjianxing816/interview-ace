import { useState } from 'react'
import { api, setToken } from '../api'
import { Button, Card, ErrorText, Field } from '../components/ui'

export default function Login({ notice, onAuthed, onGuard }) {
  const [mode, setMode] = useState('login')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const isLogin = mode === 'login'

  const submit = async (e) => {
    e?.preventDefault()
    if (busy) return
    setErr('')
    setBusy(true)
    try {
      const data = await api(`/api/auth/${isLogin ? 'login' : 'register'}`, {
        method: 'POST',
        body: JSON.stringify({ email: email.trim(), password }),
      })
      setToken(data.token)
      onAuthed(data.user)
    } catch (e2) {
      // 登录接口的 401 是"密码错"，不是"令牌失效"——
      // 交给 guard 会被当成会话过期处理，所以这里只显示错误
      if (e2.status !== 401) onGuard(e2)
      setErr(e2.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mx-auto mt-[8vh] max-w-[380px]">
      <Card className="p-6">
        <h1 className="mb-1 text-[15px] font-medium text-slate-100">
          {isLogin ? '登录' : '注册'}
        </h1>
        <p className="mb-5 text-xs text-slate-500">上传简历，让 AI 按目标岗位面试你</p>

        <form onSubmit={submit}>
          <Field label="邮箱">
            <input
              className="input-dark"
              type="email"
              autoComplete="username"
              placeholder="you@example.com"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          </Field>

          <Field label="密码（至少 8 位）">
            <input
              className="input-dark"
              type="password"
              autoComplete={isLogin ? 'current-password' : 'new-password'}
              placeholder="••••••••"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </Field>

          <Button type="submit" variant="primary" className="mt-4 w-full" disabled={busy}>
            {busy ? '请稍候…' : isLogin ? '登录' : '注册'}
          </Button>
        </form>

        <div className="mt-3 text-center">
          <Button
            variant="link"
            className="text-xs"
            onClick={() => {
              setMode(isLogin ? 'register' : 'login')
              setErr('')
            }}
          >
            {isLogin ? '没有账号？注册' : '已有账号？登录'}
          </Button>
        </div>

        <p className="mt-3 text-center text-[11px] text-slate-600">
          按 <Kbd>Enter</Kbd> 直接提交
        </p>

        {notice && !err && <p className="mt-2 text-xs text-amber-400">{notice}</p>}
        <ErrorText>{err}</ErrorText>
      </Card>
    </div>
  )
}

function Kbd({ children }) {
  return (
    <span className="rounded border border-white/[0.12] bg-white/[0.05] px-1 py-px text-[10px] text-slate-400">
      {children}
    </span>
  )
}
