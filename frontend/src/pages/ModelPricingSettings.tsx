import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { RefreshCw } from 'lucide-react'

import { api } from '@/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import { getErrorMessage } from '../utils/error'
import type { ModelPricingOverride } from '@/types'

type Row = { model: string; source: string; pricing: ModelPricingOverride }

const FIELDS: Array<{ key: keyof ModelPricingOverride; labelKey: string }> = [
  { key: 'input', labelKey: 'settings.pricing.input' },
  { key: 'cached_input', labelKey: 'settings.pricing.cached' },
  { key: 'output', labelKey: 'settings.pricing.output' },
  { key: 'input_priority', labelKey: 'settings.pricing.inputPriority' },
  { key: 'output_priority', labelKey: 'settings.pricing.outputPriority' },
  { key: 'input_long', labelKey: 'settings.pricing.inputLong' },
  { key: 'output_long', labelKey: 'settings.pricing.outputLong' },
]

interface Props {
  showToast: (message: string, kind?: 'success' | 'error') => void
}

export function ModelPricingSettings({ showToast }: Props) {
  const { t } = useTranslation()
  const [rows, setRows] = useState<Row[]>([])
  const [drafts, setDrafts] = useState<Record<string, ModelPricingOverride>>({})
  const [syncUrl, setSyncUrl] = useState('')
  const [defaultUrl, setDefaultUrl] = useState('')
  const [loading, setLoading] = useState(false)
  const [syncing, setSyncing] = useState(false)
  const [savingModel, setSavingModel] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api.listModelPricing()
      setRows(res.models)
      setDefaultUrl(res.default_sync_url)
      setSyncUrl(res.sync_url || '')
      const d: Record<string, ModelPricingOverride> = {}
      for (const r of res.models) d[r.model] = { ...r.pricing }
      setDrafts(d)
    } catch (error) {
      showToast(getErrorMessage(error), 'error')
    } finally {
      setLoading(false)
    }
  }, [showToast])

  useEffect(() => { void load() }, [load])

  const setField = (model: string, key: keyof ModelPricingOverride, value: string) => {
    const num = value.trim() === '' ? 0 : Number(value)
    setDrafts((prev) => ({ ...prev, [model]: { ...prev[model], [key]: Number.isFinite(num) ? num : 0 } }))
  }

  const save = async (model: string) => {
    setSavingModel(model)
    try {
      await api.updateModelPricing({ model, pricing: drafts[model] })
      showToast(t('settings.pricing.saved', { model }))
      await load()
    } catch (error) {
      showToast(getErrorMessage(error), 'error')
    } finally {
      setSavingModel('')
    }
  }

  const reset = async (model: string) => {
    setSavingModel(model)
    try {
      await api.updateModelPricing({ model, reset: true })
      showToast(t('settings.pricing.reset', { model }))
      await load()
    } catch (error) {
      showToast(getErrorMessage(error), 'error')
    } finally {
      setSavingModel('')
    }
  }

  const sync = async () => {
    setSyncing(true)
    try {
      const res = await api.syncModelPricing(syncUrl)
      showToast(t('settings.pricing.syncDone', { applied: res.applied, skipped: res.skipped }))
      await load()
    } catch (error) {
      showToast(`${t('settings.pricing.syncFailed')}: ${getErrorMessage(error)}`, 'error')
    } finally {
      setSyncing(false)
    }
  }

  const badgeVariant = useMemo(
    () => (source: string) => (source === 'custom' ? 'default' : source === 'synced' ? 'secondary' : 'outline'),
    [],
  )

  return (
    <div className="space-y-4">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
        <div className="flex-1 space-y-1">
          <label className="text-xs font-medium text-foreground">{t('settings.pricing.syncUrl')}</label>
          <Input
            className="font-mono text-xs"
            value={syncUrl}
            placeholder={defaultUrl}
            onChange={(e) => setSyncUrl(e.target.value)}
          />
        </div>
        <Button size="sm" variant="outline" onClick={() => void sync()} disabled={syncing}>
          <RefreshCw className={cn('size-3.5', syncing && 'animate-spin')} />
          {syncing ? t('settings.pricing.syncing') : t('settings.pricing.syncNow')}
        </Button>
      </div>
      <p className="text-[10px] text-muted-foreground">{t('settings.pricing.hint')}</p>

      <div className="space-y-2">
        {rows.map((r) => {
          const draft = drafts[r.model] ?? {}
          return (
            <div key={r.model} className="rounded-md border border-border/60 p-3">
              <div className="mb-2 flex items-center justify-between gap-2">
                <div className="flex items-center gap-2">
                  <span className="font-mono text-sm">{r.model}</span>
                  <Badge variant={badgeVariant(r.source) as 'default' | 'secondary' | 'outline'}>
                    {t(`settings.pricing.source.${r.source}`)}
                  </Badge>
                </div>
                <div className="flex gap-2">
                  <Button size="sm" variant="outline" disabled={savingModel === r.model} onClick={() => void save(r.model)}>
                    {t('common.save')}
                  </Button>
                  {r.source !== 'default' && (
                    <Button size="sm" variant="ghost" disabled={savingModel === r.model} onClick={() => void reset(r.model)}>
                      {t('settings.pricing.resetBtn')}
                    </Button>
                  )}
                </div>
              </div>
              <div className="grid grid-cols-2 gap-2 sm:grid-cols-4 xl:grid-cols-7">
                {FIELDS.map((f) => (
                  <div key={f.key} className="space-y-1">
                    <label className="text-[10px] text-muted-foreground">{t(f.labelKey)}</label>
                    <Input
                      type="number"
                      step="0.01"
                      min={0}
                      className="h-8 text-xs"
                      value={draft[f.key] ?? 0}
                      onChange={(e) => setField(r.model, f.key, e.target.value)}
                    />
                  </div>
                ))}
              </div>
            </div>
          )
        })}
        {loading && <p className="text-xs text-muted-foreground">{t('common.loading')}</p>}
      </div>
    </div>
  )
}
