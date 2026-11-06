import { useEffect, useState } from 'react'
import { api, onUnauthorized, setCsrfToken } from './api'
import { connectEvents, disconnectEvents } from './events'
import { setLanguage, t, useLanguage } from './i18n'
import { navigate, useRoute } from './router'
import { loadAccounts, loadAll, loadFolders, setState, toast, useStore } from './store'
import type { Me } from './types'
import { AccountWizard } from './components/AccountWizard'
import { ComposerHost } from './components/Composer'
import { ConfirmHost, Toasts } from './components/Dialogs'
import { Icon } from './components/Icons'
import { Login } from './components/Login'
import { MailView } from './components/MailView'
import { KeyboardHelp, NotificationsPanel, OutboxView } from './components/Panels'
import { SettingsPage } from './components/Settings'
import { Sidebar } from './components/Sidebar'

function applyTheme(theme: string | undefined) {
  const dark = theme === 'dark' || ((!theme || theme === 'system') && window.matchMedia('(prefers-color-scheme: dark)').matches)
  document.documentElement.dataset.theme = dark ? 'dark' : 'light'
}

export function App() {
  useLanguage()
  const me = useStore((s) => s.me)
  const settings = useStore((s) => s.settings)
  const [boot, setBoot] = useState<'loading' | 'ready' | 'offline'>('loading')

  const refreshMe = async () => {
    try {
      const m = await api.get<Me>('/auth/me')
      setCsrfToken(m.csrfToken)
      setState({ me: m })
      document.title = m.appName
      setBoot('ready')
      if (m.authenticated) {
        await loadAll()
        connectEvents()
      }
    } catch {
      setBoot('offline')
    }
  }

  // The favicon follows the instance logo.
  const logoVersion = me?.logoVersion
  useEffect(() => {
    const link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
    if (!link) return
    if (logoVersion) {
      link.removeAttribute('type')
      link.href = '/api/v1/branding/logo?v=' + logoVersion
    } else {
      link.type = 'image/svg+xml'
      link.href = '/favicon.svg'
    }
  }, [logoVersion])

  useEffect(() => {
    void refreshMe()
    return onUnauthorized(() => {
      disconnectEvents()
      setState((s) => ({ me: s.me ? { ...s.me, authenticated: false } : s.me }))
    })
  }, [])

  useEffect(() => {
    applyTheme(settings.theme)
    if (settings.language) setLanguage(settings.language)
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const onChange = () => applyTheme(settings.theme)
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [settings.theme, settings.language])

  if (boot === 'loading') return <div className="splash">{t('app.loading')}</div>
  if (boot === 'offline' || !me) {
    return (
      <div className="splash">
        <p>{t('err.network')}</p>
        <button className="btn" onClick={() => void refreshMe()}>
          ↻
        </button>
      </div>
    )
  }
  if (!me.authenticated) return <Login me={me} onLogin={() => void refreshMe()} />
  return <Shell onLogout={async () => {
    await api.post('/auth/logout').catch(() => {})
    disconnectEvents()
    setCsrfToken('')
    setState({ me: { ...me, authenticated: false }, accounts: [], folders: [] })
  }} />
}

function Shell({ onLogout }: { onLogout: () => void }) {
  const route = useRoute()
  const [drawer, setDrawer] = useState(false)
  const [panel, setPanel] = useState<'' | 'notif' | 'help'>('')
  const notifications = useStore((s) => s.notifications)
  const accounts = useStore((s) => s.accounts)
  const unreadNotif = notifications.filter((n) => !n.readAt).length
  const view = route.path[0] ?? ''

  // OAuth2 redirect result (passed in the URL fragment).
  useEffect(() => {
    if (view !== 'oauth-result') return
    const q = route.query
    if (q.get('status') === 'ok') {
      toast(q.get('reauth') ? t('wiz.reauthOk') : t('wiz.added'), 'success')
      if (q.get('smtpError')) toast(t('oauth.smtpWarning', { err: q.get('smtpError')! }), 'error', { ttl: 12000 })
      void Promise.allSettled([loadAccounts(), loadFolders()])
      navigate('settings/accounts?id=' + (q.get('account') ?? ''), true)
    } else {
      const code = q.get('code') ?? 'generic'
      const detail = q.get('detail') ?? ''
      const key = ('oauth.error.' + code) as never
      const msg = t(key, { detail, code })
      toast(msg === key ? t('oauth.error.generic', { code, detail }) : msg, 'error', { ttl: 15000 })
      navigate('add-account', true)
    }
  }, [view, route.query])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement
      if (e.key === '?' && !el.isContentEditable && !['INPUT', 'TEXTAREA'].includes(el.tagName)) setPanel((p) => (p === 'help' ? '' : 'help'))
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  let main
  if (view === 'settings') main = <SettingsPage />
  else if (view === 'add-account') main = <AccountWizard />
  else if (view === 'outbox') main = <OutboxView />
  else if (accounts.length === 0 && view !== 'oauth-result') main = <Welcome />
  else main = <MailView />

  return (
    <div className={'shell' + (drawer ? ' drawer-open' : '')}>
      <div className="drawer-backdrop" onClick={() => setDrawer(false)} />
      <Sidebar onNavigate={() => setDrawer(false)} onLogout={onLogout} />
      <main className="main">
        <div className="topbar">
          <button className="icon-btn menu-btn" onClick={() => setDrawer(true)} aria-label="Menu">
            <Icon name="menu" />
          </button>
          <span className="grow" />
          <button className="icon-btn" onClick={() => setPanel((p) => (p === 'notif' ? '' : 'notif'))} aria-label={t('nav.notifications')}>
            <Icon name="bell" />
            {unreadNotif > 0 && <span className="badge dot-badge">{unreadNotif}</span>}
          </button>
        </div>
        {panel === 'notif' && <NotificationsPanel onClose={() => setPanel('')} />}
        {panel === 'help' && <KeyboardHelp onClose={() => setPanel('')} />}
        {main}
      </main>
      <ComposerHost />
      <ConfirmHost />
      <Toasts />
    </div>
  )
}

function Welcome() {
  return (
    <div className="page narrow center-page">
      <Icon name="mail" size={56} className="muted" />
      <h1>{t('wiz.title')}</h1>
      <p className="muted">{t('acct.none')}</p>
      <a className="btn primary" href="#/add-account">
        <Icon name="plus" /> {t('nav.addAccount')}
      </a>
    </div>
  )
}
