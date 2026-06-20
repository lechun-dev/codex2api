import type { ReactNode } from 'react'
import { lazy, Suspense, useCallback, useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api } from '../api'
import { getTimeRangeISO, getBucketConfig, type TimeRangeKey } from '../lib/timeRange'
import PageHeader from '../components/PageHeader'
import StateShell from '../components/StateShell'
import StatCard from '../components/StatCard'
import UsageStatsSummary from '../components/UsageStatsSummary'
import type { StatsResponse, UsageStats, ChartAggregation, UsageLog } from '../types'
import { useDataLoader } from '../hooks/useDataLoader'
import { formatCompactEmail } from '../lib/utils'
import { formatBeijingTime } from '../utils/time'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Users, CheckCircle, Gauge, XCircle, Activity, AlertCircle, ExternalLink } from 'lucide-react'

const DashboardUsageCharts = lazy(() => import('../components/DashboardUsageCharts'))

const DASHBOARD_REFRESH_INTERVAL_MS = 15_000
const RECENT_ERROR_LIMIT = 5

function ChartsSkeleton() {
  return (
    <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
      {[0, 1, 2, 3].map((i) => (
        <Card key={i} className="py-0">
          <CardContent className="p-6">
            <div className="mb-5 space-y-2">
              <div className="h-4 w-32 rounded-md bg-muted animate-pulse" />
              <div className="h-3 w-48 rounded-md bg-muted/60 animate-pulse" />
            </div>
            <div className="h-[280px] flex items-end gap-2 px-4 pb-4">
              {[40, 65, 30, 80, 55, 70, 45, 60, 35, 75, 50, 68].map((h, j) => (
                <div
                  key={j}
                  className="flex-1 rounded-t-md bg-muted/50 animate-pulse"
                  style={{ height: `${h}%`, animationDelay: `${j * 80}ms` }}
                />
              ))}
            </div>
          </CardContent>
        </Card>
      ))}
    </div>
  )
}

export default function Dashboard() {
  const { t } = useTranslation()
  const [timeRange, setTimeRange] = useState<TimeRangeKey>('1h')
  const [chartData, setChartData] = useState<ChartAggregation | null>(null)
  const [chartRefreshedAt, setChartRefreshedAt] = useState<number | null>(null)
  const [chartLoading, setChartLoading] = useState(true)
  const [recentErrors, setRecentErrors] = useState<UsageLog[]>([])
  const [recentErrorsLoading, setRecentErrorsLoading] = useState(true)
  const chartAbort = useRef<AbortController | null>(null)

  // 仅加载轻量级统计数据（秒级响应）
  const loadDashboardStats = useCallback(async () => {
    const [stats, usageStats] = await Promise.all([
      api.getStats(),
      api.getUsageStats(),
    ])
    return { stats, usageStats }
  }, [])

  const { data, loading, error, reload, reloadSilently } = useDataLoader<{
    stats: StatsResponse | null
    usageStats: UsageStats | null
  }>({
    initialData: { stats: null, usageStats: null },
    load: loadDashboardStats,
  })

  // 加载服务端聚合的图表数据（12~48 个聚合点，非原始行）
  const loadChartData = useCallback(async () => {
    chartAbort.current?.abort()
    const controller = new AbortController()
    chartAbort.current = controller
    setChartLoading(true)
    try {
      const { start, end } = getTimeRangeISO(timeRange)
      const { bucketMinutes } = getBucketConfig(timeRange)
      const res = await api.getChartData({ start, end, bucketMinutes })
      if (!controller.signal.aborted) {
        setChartData(res)
        setChartRefreshedAt(Date.now())
      }
    } catch {
      // 静默容错
    } finally {
      if (!controller.signal.aborted) {
        setChartLoading(false)
      }
    }
  }, [timeRange])

  const loadRecentErrors = useCallback(async () => {
    setRecentErrorsLoading(true)
    try {
      const { start, end } = getTimeRangeISO(timeRange)
      const res = await api.getOpsErrors({
        start,
        end,
        page: 1,
        pageSize: RECENT_ERROR_LIMIT,
      })
      setRecentErrors(res.logs ?? [])
    } catch {
      setRecentErrors([])
    } finally {
      setRecentErrorsLoading(false)
    }
  }, [timeRange])

  // 首次加载 + timeRange 变更时重新拉取图表数据
  useEffect(() => {
    void loadChartData()
    void loadRecentErrors()
  }, [loadChartData, loadRecentErrors])

  // 仅在 1h（实时）模式下启用自动刷新
  useEffect(() => {
    if (timeRange !== '1h') return

    const timer = window.setInterval(() => {
      if (document.visibilityState !== 'visible') return
      void reloadSilently()
      void loadChartData()
      void loadRecentErrors()
    }, DASHBOARD_REFRESH_INTERVAL_MS)

    return () => window.clearInterval(timer)
  }, [reloadSilently, timeRange, loadChartData, loadRecentErrors])

  const { stats, usageStats } = data
  const total = stats?.total ?? 0
  const available = stats?.available ?? 0
  const rateLimited = stats?.rate_limited ?? 0
  const errorCount = stats?.error ?? 0
  const todayRequests = stats?.today_requests ?? 0

  const icons: Record<string, ReactNode> = {
    total: <Users className="size-[22px]" />,
    available: <CheckCircle className="size-[22px]" />,
    rateLimited: <Gauge className="size-[22px]" />,
    error: <XCircle className="size-[22px]" />,
    requests: <Activity className="size-[22px]" />,
  }

  return (
    <StateShell
      variant="page"
      loading={loading}
      error={error}
      onRetry={() => { void reload(); void loadChartData(); void loadRecentErrors() }}
      loadingTitle={t('dashboard.loadingTitle')}
      loadingDescription={t('dashboard.loadingDesc')}
      errorTitle={t('dashboard.errorTitle')}
    >
      <>
        <PageHeader
          title={t('dashboard.title')}
          description={t('dashboard.description')}
          onRefresh={() => { void reload(); void loadChartData(); void loadRecentErrors() }}
        />

        {/* Account status */}
        <div className="grid grid-cols-[repeat(auto-fit,minmax(220px,1fr))] gap-4 mb-6">
          <StatCard icon={icons.total} iconClass="blue" label={t('dashboard.totalAccounts')} value={total} />
          <StatCard
            icon={icons.available}
            iconClass="green"
            label={t('dashboard.available')}
            value={available}
          />
          <StatCard
            icon={icons.rateLimited}
            iconClass="amber"
            label={t('dashboard.rateLimited')}
            value={rateLimited}
          />
          <StatCard icon={icons.error} iconClass="red" label={t('dashboard.error')} value={errorCount} />
          <StatCard icon={icons.requests} iconClass="purple" label={t('dashboard.todayRequests')} value={todayRequests} />
        </div>

        {/* Usage stats */}
        {usageStats && (
          <div className="space-y-6">
            <UsageStatsSummary stats={usageStats} />
            <DashboardErrorDetails logs={recentErrors} loading={recentErrorsLoading} />
            <Suspense fallback={<ChartsSkeleton />}>
              <DashboardUsageCharts
                chartData={chartData}
                refreshedAt={chartRefreshedAt}
                refreshIntervalMs={DASHBOARD_REFRESH_INTERVAL_MS}
                timeRange={timeRange}
                onTimeRangeChange={setTimeRange}
                loading={chartLoading}
              />
            </Suspense>
          </div>
        )}
      </>
    </StateShell>
  )
}

function statusBadgeClass(statusCode: number) {
  if (statusCode >= 500) {
    return 'border-red-500/30 bg-red-500/10 text-red-600 dark:text-red-300'
  }
  if (statusCode === 429) {
    return 'border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-300'
  }
  if (statusCode >= 400) {
    return 'border-orange-500/30 bg-orange-500/10 text-orange-600 dark:text-orange-300'
  }
  return 'border-slate-500/30 bg-slate-500/10 text-slate-600 dark:text-slate-300'
}

function classifyStatus(statusCode: number) {
  if (statusCode === 401 || statusCode === 403) return 'auth_error'
  if (statusCode === 429) return 'rate_limit'
  if (statusCode === 499) return 'client_canceled'
  if (statusCode >= 500) return 'server_error'
  if (statusCode >= 400) return 'request_error'
  return 'error'
}

function formatErrorOwner(log: UsageLog) {
  const account = formatCompactEmail(log.account_email)
  if (account) return account
  if (log.api_key_name) return log.api_key_name
  if (log.api_key_masked) return log.api_key_masked
  if (log.account_id > 0) return `ID ${log.account_id}`
  return '-'
}

function DashboardErrorDetails({ logs, loading }: { logs: UsageLog[]; loading: boolean }) {
  const { t } = useTranslation()

  return (
    <Card className="py-0">
      <CardContent className="p-4">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-red-500/10 text-red-500">
                <AlertCircle className="size-4" />
              </span>
              <div className="min-w-0">
                <h3 className="truncate text-base font-semibold text-foreground">{t('dashboard.errorDetailsTitle')}</h3>
                <p className="truncate text-sm text-muted-foreground">{t('dashboard.errorDetailsDesc')}</p>
              </div>
            </div>
          </div>
          <Button asChild variant="outline" size="sm">
            <Link to="/ops/errors">
              {t('dashboard.viewAllErrors')}
              <ExternalLink className="size-3.5" />
            </Link>
          </Button>
        </div>

        {loading ? (
          <div className="space-y-2">
            {[0, 1, 2].map((item) => (
              <div key={item} className="h-10 rounded-lg bg-muted/60 animate-pulse" />
            ))}
          </div>
        ) : logs.length === 0 ? (
          <div className="rounded-lg border border-dashed border-border bg-muted/20 px-4 py-6 text-center text-sm text-muted-foreground">
            {t('dashboard.noRecentErrors')}
          </div>
        ) : (
          <div className="overflow-x-auto">
            <div className="min-w-[900px] space-y-2">
              {logs.map((log) => (
                <div
                  key={log.id}
                  className="grid grid-cols-[150px_70px_150px_130px_170px_150px_minmax(220px,1fr)] items-center gap-3 rounded-lg border border-border/70 bg-muted/20 px-3 py-2 text-sm"
                >
                  <span className="font-geist-mono text-[12px] text-muted-foreground">{formatBeijingTime(log.created_at)}</span>
                  <Badge variant="outline" className={statusBadgeClass(log.status_code)}>
                    {log.status_code}
                  </Badge>
                  <span className="truncate text-muted-foreground" title={log.upstream_error_kind || classifyStatus(log.status_code)}>
                    {log.upstream_error_kind || classifyStatus(log.status_code)}
                  </span>
                  <span className="truncate font-medium text-foreground" title={log.effective_model || log.model || '-'}>
                    {log.effective_model || log.model || '-'}
                  </span>
                  <span className="truncate font-geist-mono text-[12px] text-muted-foreground" title={log.inbound_endpoint || log.endpoint || '-'}>
                    {log.inbound_endpoint || log.endpoint || '-'}
                  </span>
                  <span className="truncate text-muted-foreground" title={formatErrorOwner(log)}>
                    {formatErrorOwner(log)}
                  </span>
                  <span className="truncate text-muted-foreground" title={log.error_message || t('opsErrors.noErrorMessage')}>
                    {log.error_message || t('opsErrors.noErrorMessage')}
                  </span>
                </div>
              ))}
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
