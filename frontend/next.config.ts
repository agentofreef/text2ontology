import type { NextConfig } from 'next'
import createNextIntlPlugin from 'next-intl/plugin'

const withNextIntl = createNextIntlPlugin('./src/i18n/request.ts')

const nextConfig: NextConfig = {
  output: 'export',
  basePath: '/lakehouse',
  // Strip the "X-Powered-By: Next.js" response header — it leaks the framework
  // (and a version hint) to attackers fingerprinting the stack for no benefit.
  poweredByHeader: false,
  images: {
    unoptimized: true,
  },
}

export default withNextIntl(nextConfig)
