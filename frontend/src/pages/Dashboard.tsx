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
import TimeRangeSelector from '../components/TimeRangeSelector'
import ChannelFilter, { useUsageChannel, type UsageChannel } from '../components/ChannelFilter'
import ChannelLogo from '../components/ChannelLogo'
import SystemHealthBar from '../components/SystemHealthBar'
import type {
  AccountRow,
  OpsOverviewResponse,
  StatsResponse,
  StatsChannelCounts,
  SystemSettings,
  UsageStats,
  ChartAggregation,
  UsageLog,
} from '../types'
import { useDataLoader } from '../hooks/useDataLoader'
import { formatCompactEmail } from '../lib/utils'
import { formatBeijingTime } from '../utils/time'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { BarChart3, Users, CheckCircle, Gauge, XCircle, Activity, AlertCircle, ExternalLink } from 'lucide-react'
import PoolRunwayCard from '../components/PoolRunwayCard'

const DashboardUsageCharts = lazy(() => import('../components/DashboardUsageCharts'))

const DASHBOARD_REFRESH_INTERVAL_MS = 15_000
const RECENT_ERROR_LIMIT = 5
const DASHBOARD_POOL_RUNWAY_VISIBILITY_KEY = 'codex2api:dashboard:pool-runway-visible'

function getInitialPoolRunwayVisibility(): boolean {
  try {
    return window.localStorage.getItem(DASHBOARD_POOL_RUNWAY_VISIBILITY_KEY) !== 'false'
  } catch {
    return true
  }
}

function persistPoolRunwayVisibility(visible: boolean) {
  try {
    window.localStorage.setItem(
      DASHBOARD_POOL_RUNWAY_VISIBILITY_KEY,
      visible ? 'true' : 'false',
    )
  } catch {
    // Restricted browser modes may block localStorage; keep in-memory toggle working.
  }
}

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
  const [channel, setChannel] = useUsageChannel()
  const channelRef = useRef<UsageChannel>(channel)
  const [showPoolRunway, setShowPoolRunway] = useState(getInitialPoolRunwayVisibility)
  const [chartData, setChartData] = useState<ChartAggregation | null>(null)
  const [chartRefreshedAt, setChartRefreshedAt] = useState<number | null>(null)
  const [chartLoading, setChartLoading] = useState(true)
  const [recentErrors, setRecentErrors] = useState<UsageLog[]>([])
  const [recentErrorsLoading, setRecentErrorsLoading] = useState(true)
  const chartAbort = useRef<AbortController | null>(null)
  const timeRangeRef = useRef<TimeRangeKey>(timeRange)
  const usageStatsRangeInitialized = useRef(false)
  const showPoolRunwayRef = useRef(showPoolRunway)

  // 统计始终加载；号池分析仅在开启时拉账号列表 + ops RPM（隐藏时省流量）
  const loadDashboardStats = useCallback(async () => {
    const { start, end } = getTimeRangeISO(timeRangeRef.current)
    const includePoolRunway = showPoolRunwayRef.current
    const [stats, usageStats, settings, accountsRes, opsOverview] = await Promise.all([
      api.getStats(),
      api.getUsageStats({ start, end, channel: channelRef.current || undefined }),
      api.getSettings().catch((): SystemSettings | null => null),
      includePoolRunway
        ? api.getAccounts().catch(() => ({ accounts: [] as AccountRow[] }))
        : Promise.resolve({ accounts: [] as AccountRow[] }),
      includePoolRunway
        ? api.getOpsOverview().catch((): OpsOverviewResponse | null => null)
        : Promise.resolve(null),
    ])
    return {
      stats,
      usageStats,
      settings,
      accounts: accountsRes.accounts ?? [],
      opsOverview,
    }
  }, [])

  const { data, loading, error, reload, reloadSilently, setData } = useDataLoader<{
    stats: StatsResponse | null
    usageStats: UsageStats | null
    settings: SystemSettings | null
    accounts: AccountRow[]
    opsOverview: OpsOverviewResponse | null
  }>({
    initialData: {
      stats: null,
      usageStats: null,
      settings: null,
      accounts: [],
      opsOverview: null,
    },
    load: loadDashboardStats,
  })

  // 偏好持久化 + 开关切换时补拉/清空（跳过首屏，避免与 useDataLoader 首拉重复）
  const poolRunwayToggleReady = useRef(false)
  useEffect(() => {
    showPoolRunwayRef.current = showPoolRunway
    persistPoolRunwayVisibility(showPoolRunway)
    if (!poolRunwayToggleReady.current) {
      poolRunwayToggleReady.current = true
      return
    }
    if (!showPoolRunway) {
      setData((prev) => ({ ...prev, accounts: [], opsOverview: null }))
      return
    }
    void reloadSilently()
  }, [showPoolRunway, reloadSilently, setData])

  useEffect(() => {
    timeRangeRef.current = timeRange
    channelRef.current = channel
    if (!usageStatsRangeInitialized.current) {
      usageStatsRangeInitialized.current = true
      return
    }
    void reloadSilently()
  }, [timeRange, channel, reloadSilently])

  // 加载服务端聚合的图表数据（12~48 个聚合点，非原始行）
  const loadChartData = useCallback(async () => {
    chartAbort.current?.abort()
    const controller = new AbortController()
    chartAbort.current = controller
    setChartLoading(true)
    try {
      const { start, end } = getTimeRangeISO(timeRange)
      const { bucketMinutes } = getBucketConfig(timeRange)
      const res = await api.getChartData({ start, end, bucketMinutes, channel: channel || undefined })
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
  }, [timeRange, channel])

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

  const { stats, usageStats, settings, accounts, opsOverview } = data
  const showFullUsageNumbers = settings?.show_full_usage_numbers ?? false
  // 渠道视图下账号池概览与统计卡切换为该渠道的计数；全部视图保持总量并展示分渠道徽标。
  // 旧后端响应无 channels 字段时回退全量，有字段但该渠道无账号时如实显示 0。
  const emptyChannelCounts: StatsChannelCounts = {
    total: 0, available: 0, rate_limited: 0, error: 0, today_requests: 0,
  }
  const effectiveCounts = channel && stats?.channels
    ? (stats.channels[channel] ?? emptyChannelCounts)
    : stats
  const total = effectiveCounts?.total ?? 0
  const available = effectiveCounts?.available ?? 0
  const rateLimited = effectiveCounts?.rate_limited ?? 0
  const errorCount = effectiveCounts?.error ?? 0
  const todayRequests = effectiveCounts?.today_requests ?? 0
  const channelBreakdown = !channel && stats?.channels
    ? (['codex', 'grok'] as const)
        .map((key) => ({ key, counts: stats.channels?.[key] }))
        .filter((item): item is { key: 'codex' | 'grok'; counts: StatsChannelCounts } =>
          Boolean(item.counts && item.counts.total > 0))
    : []
  const currentRpm = opsOverview?.traffic?.rpm ?? 0
  const rpmLimit = opsOverview?.traffic?.rpm_limit ?? 0
  const avgDurationMs = opsOverview?.traffic?.avg_duration_ms ?? 0

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
		  titleAdornment={<ChannelFilter value={channel} onChange={setChannel} />}
          actions={
            <div className="flex flex-wrap items-center gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="shrink-0"
                aria-pressed={showPoolRunway}
                onClick={() => setShowPoolRunway((visible) => !visible)}
                title={
                  showPoolRunway
                    ? t('dashboard.hidePoolRunway')
                    : t('dashboard.showPoolRunway')
                }
              >
                <BarChart3 className="size-3.5" />
                <span className="hidden sm:inline">
                  {showPoolRunway
                    ? t('dashboard.hidePoolRunway')
                    : t('dashboard.showPoolRunway')}
                </span>
              </Button>
              <TimeRangeSelector
                timeRange={timeRange}
                onTimeRangeChange={setTimeRange}
              />
            </div>
          }
        />

        {/* 渠道切换时整块内容淡入过渡（key 变化触发重播） */}
        <div key={channel || 'all'} className="animate-channel-switch-in">
        {/* Hero summary */}
        <div className="relative mb-5 overflow-hidden rounded-2xl border border-border/80 bg-card p-4 shadow-sm sm:mb-6 sm:p-5">
          <div
            aria-hidden
            className="pointer-events-none absolute inset-0 bg-[radial-gradient(ellipse_at_top_left,color-mix(in_oklab,var(--color-primary)_12%,transparent),transparent_55%)]"
          />
          <div className="relative z-10 flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
            <div className="min-w-0">
              <div className="text-[11px] font-bold uppercase tracking-wide text-muted-foreground">
                {t('dashboard.heroLabel')}
              </div>
              <div className="mt-1 flex flex-wrap items-end gap-x-3 gap-y-1">
                <div className="text-3xl font-bold tabular-nums tracking-tight text-foreground sm:text-4xl">
                  {available}
                  <span className="text-lg font-semibold text-muted-foreground sm:text-xl">/{total}</span>
                </div>
                <div className="pb-1 text-sm text-muted-foreground">
                  {t('dashboard.heroAvailable')}
                </div>
              </div>
              <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <span className="inline-flex items-center gap-1.5 rounded-full bg-emerald-500/12 px-2.5 py-1 font-semibold text-emerald-700 dark:text-emerald-300">
                  <span className="size-1.5 rounded-full bg-emerald-500" />
                  {total > 0
                    ? t('dashboard.heroAvailability', {
                        rate: Math.round((available / Math.max(total, 1)) * 100),
                      })
                    : t('dashboard.heroNoAccounts')}
                </span>
                <span className="inline-flex items-center rounded-full bg-muted/80 px-2.5 py-1 font-medium">
                  {t('dashboard.heroTodayRequests', { count: todayRequests })}
                </span>
                {channelBreakdown.map(({ key, counts }) => (
                  <span
                    key={key}
                    className="inline-flex items-center gap-1.5 rounded-full bg-muted/80 px-2.5 py-1 font-medium"
                    title={t('dashboard.heroChannelTitle', {
                      channel: key === 'grok' ? 'Grok' : 'Codex',
                      available: counts.available,
                      total: counts.total,
                      requests: counts.today_requests,
                    })}
                  >
                    <ChannelLogo channel={key} size={13} />
                    <span className="tabular-nums">
                      {counts.available}/{counts.total}
                    </span>
                    <span className="text-muted-foreground/70">·</span>
                    <span className="tabular-nums">{counts.today_requests}</span>
                  </span>
                ))}
                {errorCount > 0 ? (
                  <span className="inline-flex items-center rounded-full bg-destructive/12 px-2.5 py-1 font-semibold text-destructive">
                    {t('dashboard.heroErrors', { count: errorCount })}
                  </span>
                ) : null}
                {rateLimited > 0 ? (
                  <span className="inline-flex items-center rounded-full bg-amber-500/12 px-2.5 py-1 font-semibold text-amber-700 dark:text-amber-300">
                    {t('dashboard.heroRateLimited', { count: rateLimited })}
                  </span>
                ) : null}
              </div>
            </div>
            {total === 0 ? (
              <div className="rounded-xl border border-dashed border-border bg-background/70 px-4 py-3 text-left text-sm text-muted-foreground lg:max-w-sm">
                <div className="font-semibold text-foreground">{t('dashboard.heroEmptyTitle')}</div>
                <p className="mt-1 leading-relaxed">{t('dashboard.heroEmptyDesc')}</p>
              </div>
            ) : null}
          </div>
        </div>

        {/* Account status */}
        <div className="mb-6 grid grid-cols-1 gap-3 min-[420px]:grid-cols-2 xl:grid-cols-5 sm:gap-4">
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

        {/* Pool runway（可开关）+ system health */}
        <div className="mb-6 space-y-3">
          {showPoolRunway && accounts.length > 0 ? (
            <PoolRunwayCard
              accounts={accounts}
              currentRpm={currentRpm}
              rpmLimit={rpmLimit}
              avgDurationMs={avgDurationMs}
            />
          ) : null}
          <SystemHealthBar chartData={chartData} timeRange={timeRange} loading={chartLoading} />
        </div>

        {/* Usage stats */}
        {usageStats && (
          <div className="space-y-6">
            <UsageStatsSummary
              stats={usageStats}
              rangeLabel={t(`dashboard.timeRange${timeRange.toUpperCase()}`)}
              showFullUsageNumbers={showFullUsageNumbers}
            />
            <DashboardErrorDetails logs={recentErrors} loading={recentErrorsLoading} />
            <Suspense fallback={<ChartsSkeleton />}>
              <DashboardUsageCharts
                chartData={chartData}
                refreshedAt={chartRefreshedAt}
                refreshIntervalMs={DASHBOARD_REFRESH_INTERVAL_MS}
                timeRange={timeRange}
                loading={chartLoading}
              />
            </Suspense>
          </div>
        )}
        </div>
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
  if (statusCode === 401) return 'unauthorized'
  if (statusCode === 403) return 'payment_required'
  if (statusCode === 429) return 'rate_limited'
  if (statusCode === 499) return 'client_canceled'
  if (statusCode >= 500) return 'server'
  if (statusCode >= 400) return 'client'
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
