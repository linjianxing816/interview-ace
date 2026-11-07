import { useCallback, useEffect, useState } from 'react'
import { ApiError, api, getToken, setToken } from './api'
import TopBar from './components/TopBar'
import Login from './pages/Login'
import Home from './pages/Home'
import Chat from './pages/Chat'
import Report from './pages/Report'

// 单页应用只有四个视图，用 state 切换就够了，不引 react-router。
// 引入路由库的唯一收益是 URL 可分享，而这个项目不需要（没有对外分享的页面）。
export default function App() {
  const [view, setView] = useState(getToken() ? 'home' : 'auth')
  const [email, setEmail] = useState('')
  const [notice, setNotice] = useState('')
  const [activeId, setActiveId] = useState(null)

  // 令牌失效（过期 / 账号被删）统一在这里处理：
  // 清令牌、回登录页、把原因显示在登录框下面。
  // 每个页面各写一遍的话，总有漏掉的地方。
  const guard = useCallback((err) => {
    if (err instanceof ApiError && err.status === 401) {
      setToken('')
      setEmail('')
      setNotice(err.message)
      setView('auth')
      return true
    }
    return false
  }, [])

  const logout = useCallback(() => {
    setToken('')
    setEmail('')
    setActiveId(null)
    setNotice('')
    setView('auth')
  }, [])

  useEffect(() => {
    if (!getToken()) return
    // 用一次轻量请求确认令牌还有效，避免带着废令牌进首页后每个请求都 401
    api('/api/resumes')
      .then(() => setView('home'))
      .catch((e) => {
        if (!guard(e)) setView('home')
      })
  }, [guard])

  const onAuthed = (user) => {
    setEmail(user?.email || '')
    setNotice('')
    setView('home')
  }

  const openInterview = (id, target = 'chat') => {
    setActiveId(id)
    setView(target)
  }

  return (
    <div className="min-h-screen">
      <TopBar email={email} showLogout={view !== 'auth'} onLogout={logout} />

      <main className="mx-auto max-w-[880px] px-5 pb-20 pt-6">
        {view === 'auth' && <Login notice={notice} onAuthed={onAuthed} onGuard={guard} />}

        {view === 'home' && (
          <Home onOpen={openInterview} onLogout={logout} onGuard={guard} />
        )}

        {view === 'chat' && activeId && (
          <Chat
            id={activeId}
            onBack={() => setView('home')}
            onReport={() => setView('report')}
            onGuard={guard}
          />
        )}

        {view === 'report' && activeId && (
          <Report id={activeId} onBack={() => setView('home')} onGuard={guard} />
        )}
      </main>
    </div>
  )
}
