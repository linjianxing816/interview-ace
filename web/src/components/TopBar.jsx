export default function TopBar({ email, showLogout, onLogout }) {
  return (
    <header className="sticky top-0 z-10 border-b border-white/[0.07] bg-ink-900/85 backdrop-blur">
      <div className="mx-auto flex h-[52px] max-w-[880px] items-center gap-3 px-5">
        <span className="grad-text text-sm font-medium">InterviewAce</span>
        <span className="flex-1" />
        {email && <span className="max-w-[220px] truncate text-xs text-slate-500">{email}</span>}
        {showLogout && (
          <button
            onClick={onLogout}
            className="text-xs text-grape transition hover:text-grape/80"
          >
            退出
          </button>
        )}
      </div>
    </header>
  )
}
