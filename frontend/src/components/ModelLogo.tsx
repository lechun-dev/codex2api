import { useEffect, useMemo, useState } from 'react'
import { cn } from '@/lib/utils'

/**
 * LobeHub Icons CDN — https://lobehub.com/icons
 * Uses static SVG (no @lobehub/icons / antd peer deps).
 */
const LOBE_ICON_BASE = 'https://unpkg.com/@lobehub/icons-static-svg@latest/icons'

type Mapping = {
  /** LobeHub static icon id (lowercase), e.g. "openai", "claude" */
  id: string
  /** Prefer color variant when available */
  color?: boolean
  /** Regex keywords matched against the lowercased model id */
  keywords: string[]
}

// Order matters: more specific patterns first (mirrors @lobehub/icons modelMappings).
const MODEL_MAPPINGS: Mapping[] = [
  { id: 'sora', color: true, keywords: ['sora'] },
  { id: 'dalle', color: true, keywords: ['dalle', 'dall-e'] },
  { id: 'codex', color: true, keywords: ['codex'] },
  { id: 'openai', color: false, keywords: ['gpt-oss', 'text-embedding-', 'tts-', 'whisper-', 'davinci', 'babbage', 'omni-moderation', 'computer-use'] },
  { id: 'openai', color: false, keywords: ['o1-', '^o1', '/o1', 'o3-', '^o3', '/o3', 'o4-', '^o4', '/o4'] },
  { id: 'openai', color: false, keywords: ['gpt-3', 'gpt-4', 'gpt-5', '^gpt-', '/gpt-', 'openai'] },
  { id: 'claude', color: true, keywords: ['claude'] },
  { id: 'anthropic', color: true, keywords: ['anthropic'] },
  { id: 'gemini', color: true, keywords: ['gemini'] },
  { id: 'gemma', color: true, keywords: ['gemma'] },
  { id: 'deepmind', color: true, keywords: ['^imagen-', '/imagen-', 'deepmind'] },
  { id: 'deepseek', color: true, keywords: ['deepseek'] },
  { id: 'qwen', color: true, keywords: ['qwen', 'qwq', 'qvq', 'tongyi'] },
  { id: 'moonshot', color: true, keywords: ['kimi', 'moonshot'] },
  { id: 'mistral', color: true, keywords: ['mistral', 'mixtral', 'codestral', 'pixtral', 'ministral', 'magistral', 'devstral', 'voxtral'] },
  { id: 'meta', color: true, keywords: ['llama', '/l3'] },
  { id: 'llava', color: true, keywords: ['llava'] },
  { id: 'grok', color: false, keywords: ['^grok-', '/grok-', 'grok'] },
  { id: 'perplexity', color: true, keywords: ['pplx', 'sonar', 'perplexity'] },
  { id: 'yi', color: true, keywords: ['^yi-', '/yi-', '-yi-'] },
  { id: 'minimax', color: true, keywords: ['minimax', 'abab'] },
  { id: 'zai', color: true, keywords: ['^glm-5', '/glm-5', '-glm-5', '^glm-4', '/glm-4', '-glm-4', '/glm4', '/glm5'] },
  { id: 'chatglm', color: true, keywords: ['chatglm', '^glm-', '/glm-', '-glm-'] },
  { id: 'doubao', color: true, keywords: ['doubao-', '^ep-'] },
  { id: 'hunyuan', color: true, keywords: ['hunyuan'] },
  { id: 'wenxin', color: true, keywords: ['ernie', 'wenxin'] },
  { id: 'baichuan', color: true, keywords: ['baichuan'] },
  { id: 'spark', color: true, keywords: ['spark', 'xinghuo'] },
  { id: 'stepfun', color: true, keywords: ['^step', '/step'] },
  { id: 'cohere', color: true, keywords: ['command', 'cohere'] },
  { id: 'nvidia', color: true, keywords: ['nemotron', 'nv-', 'nvidia'] },
  { id: 'microsoft', color: true, keywords: ['wizardlm', '/phi-', '^phi-', '-phi-', 'mai-', 'microsoft'] },
  { id: 'stability', color: true, keywords: ['stable-diffusion', 'sdxl', 'stablelm', '^sd3', '^sd2', '^sd1'] },
  { id: 'flux', color: true, keywords: ['flux'] },
  { id: 'openrouter', color: true, keywords: ['^openrouter', 'openrouter'] },
  { id: 'aws', color: true, keywords: ['titan', 'bedrock', 'amazon'] },
  { id: 'google', color: true, keywords: ['google', 'learnlm'] },
  { id: 'xiaomimimo', color: true, keywords: ['^mimo-', '/mimo-'] },
  { id: 'internlm', color: true, keywords: ['internlm', 'internvl'] },
  { id: 'jina', color: true, keywords: ['^jina', '/jina'] },
  { id: 'voyage', color: true, keywords: ['voyage'] },
  { id: 'fireworks', color: true, keywords: ['fireworks'] },
  { id: 'together', color: true, keywords: ['together'] },
  { id: 'ollama', color: true, keywords: ['ollama'] },
  { id: 'huggingface', color: true, keywords: ['huggingface', 'hf.co', 'hugging'] },
]

// Icons known to lack a -color.svg asset on the static pack.
const NO_COLOR = new Set(['openai', 'grok', 'metagpt'])

export function resolveLobeIconId(model: string): { id: string; color: boolean } {
  const value = model.trim().toLowerCase()
  if (!value) return { id: 'openai', color: false }

  for (const item of MODEL_MAPPINGS) {
    if (item.keywords.some((kw) => {
      try {
        return new RegExp(kw, 'i').test(value)
      } catch {
        return value.includes(kw.toLowerCase())
      }
    })) {
      const color = Boolean(item.color) && !NO_COLOR.has(item.id)
      return { id: item.id, color }
    }
  }

  return { id: 'openai', color: false }
}

export function getLobeIconUrl(model: string, preferColor = true): string {
  const resolved = resolveLobeIconId(model)
  const useColor = preferColor && resolved.color
  return `${LOBE_ICON_BASE}/${resolved.id}${useColor ? '-color' : ''}.svg`
}

function modelInitials(model: string): string {
  const parts = model
    .replace(/^gpt-?/i, '')
    .split(/[-_.\s/]+/)
    .filter(Boolean)
  if (parts.length === 0) return 'AI'
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase()
  return (parts[0][0] + parts[1][0]).toUpperCase()
}

function modelAccent(model: string): string {
  let hash = 0
  for (let i = 0; i < model.length; i += 1) hash = (hash * 31 + model.charCodeAt(i)) >>> 0
  const hues = [214, 198, 262, 160, 32, 280, 190]
  return `hsl(${hues[hash % hues.length]} 72% 48%)`
}

interface ModelLogoProps {
  model: string
  size?: number
  className?: string
  /** 'soft' = rounded square with muted bg; 'plain' = bare icon; 'ring' = bordered tile */
  variant?: 'soft' | 'plain' | 'ring'
  title?: string
}

export default function ModelLogo({
  model,
  size = 28,
  className,
  variant = 'soft',
  title,
}: ModelLogoProps) {
  const [failed, setFailed] = useState(false)
  const [triedMono, setTriedMono] = useState(false)

  const resolved = useMemo(() => resolveLobeIconId(model), [model])

  useEffect(() => {
    setFailed(false)
    setTriedMono(false)
  }, [model, resolved.id])

  const src = useMemo(() => {
    if (failed && triedMono) return null
    if (failed || triedMono || !resolved.color) {
      return `${LOBE_ICON_BASE}/${resolved.id}.svg`
    }
    return `${LOBE_ICON_BASE}/${resolved.id}-color.svg`
  }, [failed, resolved.color, resolved.id, triedMono])

  const pad = variant === 'plain' ? 0 : Math.max(4, Math.round(size * 0.18))
  const iconSize = size - pad * 2

  if (!src || (failed && triedMono)) {
    const accent = modelAccent(model)
    return (
      <span
        title={title ?? model}
        className={cn(
          'inline-flex shrink-0 items-center justify-center font-bold tracking-wide text-white',
          variant === 'plain' ? 'rounded-md' : 'rounded-xl',
          className,
        )}
        style={{
          width: size,
          height: size,
          fontSize: Math.max(10, Math.round(size * 0.32)),
          background: `linear-gradient(145deg, ${accent}, color-mix(in oklab, ${accent} 55%, #0f172a))`,
        }}
        aria-hidden
      >
        {modelInitials(model)}
      </span>
    )
  }

  return (
    <span
      title={title ?? model}
      className={cn(
        'inline-flex shrink-0 items-center justify-center overflow-hidden',
        variant === 'soft' && 'rounded-xl bg-muted/70 ring-1 ring-border/70',
        variant === 'ring' && 'rounded-xl bg-card ring-1 ring-border shadow-sm',
        variant === 'plain' && 'rounded-md',
        className,
      )}
      style={{ width: size, height: size }}
      aria-hidden
    >
      <img
        src={src}
        alt=""
        width={iconSize}
        height={iconSize}
        draggable={false}
        className={cn(
          'object-contain',
          // Mono OpenAI mark is black; invert in dark mode for contrast.
          resolved.id === 'openai' && 'dark:invert dark:opacity-90',
          resolved.id === 'grok' && 'dark:invert dark:opacity-90',
        )}
        style={{ width: iconSize, height: iconSize }}
        loading="lazy"
        decoding="async"
        referrerPolicy="no-referrer"
        onError={() => {
          if (!triedMono && resolved.color) {
            setTriedMono(true)
            return
          }
          setFailed(true)
        }}
      />
    </span>
  )
}
