import type { Metadata, Viewport } from 'next'
import { Inter, Fira_Code } from 'next/font/google'
import { Analytics } from '@vercel/analytics/next'
import { defaultLocale, messages } from '@/lib/i18n'
import './globals.css'

const inter = Inter({
  subsets: ['latin'],
  variable: '--font-inter',
  display: 'swap',
})

const firaCode = Fira_Code({
  subsets: ['latin'],
  variable: '--font-mono-code',
  display: 'swap',
})

export const metadata: Metadata = {
  title: messages[defaultLocale]['meta.title'],
  description: messages[defaultLocale]['meta.description'],
  generator: 'v0.app',
}

export const viewport: Viewport = {
  colorScheme: 'dark',
  themeColor: '#111114',
}

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode
}>) {
  return (
    <html lang={defaultLocale} className="bg-background">
      <body className={`${inter.variable} ${firaCode.variable} font-sans antialiased`}>
        {children}
        {process.env.VERCEL === '1' && <Analytics />}
      </body>
    </html>
  )
}
