// Root layout — bare Server Component. Single canonical <html>/<body>.
// All app providers (AuthProvider / ProjectProvider / MessageProvider / AppShell)
// live in `[locale]/ClientProviders.tsx` to keep this layout free of hook usage.
// `<html lang>` is statically `zh-CN` here; `LangSync` client component updates
// `document.documentElement.lang` on hydration based on the active locale.
import './globals.css'

export const metadata = {
  title: 'text2ontology',
}

export default function RootLayout({
  children,
}: {
  children: React.ReactNode
}) {
  return (
    <html lang="zh-CN">
      <head>
        <link rel="icon" href="/lakehouse/favicon.svg" type="image/svg+xml" />
        <link rel="icon" href="/lakehouse/logo.svg" type="image/svg+xml" sizes="any" />
      </head>
      <body className="min-h-screen bg-canvas">{children}</body>
    </html>
  )
}
