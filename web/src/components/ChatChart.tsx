import { useEffect, useMemo, useRef, useState } from 'react'
import type { Chart as ChartJS } from 'chart.js'
import { Loader, Maximize2, Table2, TriangleAlert } from 'lucide-react'
import { useWidth } from '@/components/charts'
import { Lightbox } from '@/components/ui/lightbox'
import { cn } from '@/lib/utils'
import { getLocale, t } from '@/lib/i18n'

// 助手在回答里写的 ```chart 代码块（JSON）画成柱状图 / 折线图。格式写在系统提示里（internal/agent/agent.go）：
// { type: bar | line, title, x: 横轴字段, y: 数值字段或数组, labels?: {字段: 中文名}, unit?, data: [行] }

export type ChartSpec = {
  type: 'bar' | 'line'
  title: string
  x: string
  y: string[]
  labels: Record<string, string>
  unit: string
  data: Record<string, unknown>[]
}

const MAX_ROWS = 200
const MAX_SERIES = 4

/** 解析并检查 chart 块，返回画图用的 spec，或者说明哪里不对 */
export function parseChart(src: string): { spec: ChartSpec } | { error: string } {
  let raw: Record<string, unknown>
  try {
    raw = JSON.parse(src)
  } catch (e) {
    return { error: t('不是合法的 JSON（{msg}）', { msg: (e as Error).message }) }
  }
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return { error: t('要是一个 JSON 对象') }
  const type = raw.type
  if (type !== 'bar' && type !== 'line') return { error: t('type 只能是 bar 或 line，收到 {v}', { v: JSON.stringify(type) }) }
  const x = raw.x
  if (typeof x !== 'string' || !x) return { error: t('x 要写横轴的字段名') }
  const y = typeof raw.y === 'string' ? [raw.y] : raw.y
  if (!Array.isArray(y) || y.length === 0 || y.some((f) => typeof f !== 'string')) return { error: t('y 要写数值字段名（一个或数组）') }
  if (y.length > MAX_SERIES) return { error: t('y 最多 {max} 个字段，收到 {n} 个', { max: MAX_SERIES, n: y.length }) }
  const data = raw.data
  if (!Array.isArray(data) || data.length === 0) return { error: t('data 要是非空的行数组') }
  if (data.length > MAX_ROWS) return { error: t('data 有 {n} 行，最多 {max} 行', { n: data.length, max: MAX_ROWS }) }
  for (const [i, row] of data.entries()) {
    if (!row || typeof row !== 'object') return { error: t('第 {i} 行不是对象', { i: i + 1 }) }
    const r = row as Record<string, unknown>
    if (typeof r[x] !== 'string' && typeof r[x] !== 'number') return { error: t('第 {i} 行没有横轴字段 {x}', { i: i + 1, x }) }
    for (const f of y as string[]) {
      const v = r[f]
      if (v !== null && v !== undefined && (typeof v !== 'number' || !Number.isFinite(v))) return { error: t('第 {i} 行的 {f} 是 {v}，要数字', { i: i + 1, f, v: JSON.stringify(v) }) }
    }
  }
  for (const f of y as string[]) {
    if (!data.some((r) => typeof (r as Record<string, unknown>)[f] === 'number')) return { error: t('没有一行有 {f} 的数值，检查字段名', { f }) }
  }
  const labels = raw.labels && typeof raw.labels === 'object' ? (raw.labels as Record<string, string>) : {}
  return {
    spec: {
      type,
      title: typeof raw.title === 'string' ? raw.title : '',
      x,
      y: y as string[],
      labels,
      unit: typeof raw.unit === 'string' ? raw.unit : '',
      data: data as Record<string, unknown>[],
    },
  }
}

/** 轴上的数：1.2万、3.4亿 */
function compact(v: number) {
  const a = Math.abs(v)
  if (getLocale() === 'en') {
    if (a >= 1e9) return `${+(v / 1e9).toFixed(1)}B`
    if (a >= 1e6) return `${+(v / 1e6).toFixed(1)}M`
    if (a >= 1e4) return `${+(v / 1e3).toFixed(1)}K`
    return v.toLocaleString('en-US', { maximumFractionDigits: 2 })
  }
  if (a >= 1e8) return `${+(v / 1e8).toFixed(1)}亿`
  if (a >= 1e4) return `${+(v / 1e4).toFixed(1)}万`
  return v.toLocaleString('zh-CN', { maximumFractionDigits: 2 })
}

// 按 11px 字号量标签宽度、截到 w 像素以内
let measure: CanvasRenderingContext2D | null = null
function textWidth(s: string) {
  measure ??= document.createElement('canvas').getContext('2d')
  if (!measure) return s.length * 7
  measure.font = `11px ${getComputedStyle(document.body).fontFamily}`
  return measure.measureText(s).width
}
function fit(label: string, w: number) {
  if (textWidth(label) <= w) return label
  const chars = [...label]
  while (chars.length > 1 && textWidth(chars.join('') + '…') > w) chars.pop()
  return chars.join('') + '…'
}

const fmtFull = (v: number, unit: string) => v.toLocaleString('zh-CN', { maximumFractionDigits: 2 }) + (unit ? ` ${unit}` : '')

function palette() {
  const s = getComputedStyle(document.documentElement)
  const v = (n: string) => s.getPropertyValue(n).trim()
  return {
    series: [v('--viz-1'), v('--viz-2'), v('--viz-3'), v('--viz-4')],
    text: v('--muted-foreground'),
    grid: v('--border'),
    surface: v('--background'),
    tipBg: v('--popover'),
    tipText: v('--popover-foreground'),
    tipBorder: v('--input'),
  }
}

// 主题切换（<html data-theme>）时重画
function useThemeKey() {
  const [k, setK] = useState(() => document.documentElement.getAttribute('data-theme') ?? '')
  useEffect(() => {
    const el = document.documentElement
    const mo = new MutationObserver(() => setK(el.getAttribute('data-theme') ?? ''))
    mo.observe(el, { attributes: true, attributeFilter: ['data-theme'] })
    return () => mo.disconnect()
  }, [])
  return k
}

/** live：这条回答还在输出，块没写完时显示「正在画图」而不是报错 */
export default function ChatChart({ src, live }: { src: string; live: boolean }) {
  const parsed = useMemo(() => parseChart(src), [src])
  if ('error' in parsed) {
    if (live)
      return (
        <div className="my-2 flex h-24 items-center justify-center gap-2 rounded-lg border border-border text-xs text-muted-foreground">
          <Loader className="size-3.5 animate-spin" /> {t('正在画图…')}
        </div>
      )
    return (
      <div className="my-2 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive">
        <div className="flex items-center gap-1.5 font-medium">
          <TriangleAlert className="size-3.5" /> {t('图没画出来：')}{parsed.error}
        </div>
      </div>
    )
  }
  return <ChartView spec={parsed.spec} />
}

const tool = 'inline-flex size-6 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground'

/** full：在全屏层里画的大图 */
function ChartView({ spec, full }: { spec: ChartSpec; full?: boolean }) {
  const canvas = useRef<HTMLCanvasElement>(null)
  const [table, setTable] = useState(false)
  const [zoom, setZoom] = useState(false)
  const theme = useThemeKey()
  // 量整张卡片（切到表格也一直在），减掉 p-3 的内边距就是画布宽
  const [box, outer] = useWidth<HTMLElement>()
  const width = Math.max(0, outer - 26)
  const name = (f: string) => spec.labels[f] ?? f
  const cats = useMemo(() => spec.data.map((r) => String(r[spec.x])), [spec])
  const line = spec.type === 'line'
  // 柱状图每个分类都要看得清名字：竖排的柱子下面放不下（平均每个分类不到 4 个字宽），就横过来，名字在左边一行一个
  const horiz = !line && width > 0 && cats.some((l) => textWidth(l) > (width - 40) / cats.length - 6) && (width - 40) / cats.length < 52
  const rowH = Math.max(22, spec.y.length * 10 + 10)
  const fullH = Math.round(window.innerHeight * 0.65)
  const height = horiz ? Math.max(cats.length * rowH + 28 + (spec.y.length > 1 ? 24 : 0), full ? Math.min(fullH, cats.length * 44) : 0) : full ? fullH : 192

  useEffect(() => {
    if (table || !canvas.current || width === 0) return
    let chart: ChartJS | undefined
    let dead = false
    import('@/lib/chartjs').then(({ Chart }) => {
      if (dead || !canvas.current) return
      const c = palette()
      const dense = spec.data.length > (full ? 60 : 24)
      // 柱状图的分类不自动隐藏，放不下就截断（完整的在悬停提示里）；折线的日期照常隔几个显示
      const keepAll = !line
      const catTicks = {
        color: c.text,
        font: { size: 11 },
        maxRotation: 0,
        autoSkip: !keepAll,
        autoSkipPadding: 8,
      }
      const valAxis = {
        beginAtZero: true,
        grid: { color: c.grid },
        border: { display: false },
        ticks: { color: c.text, font: { size: 11 }, maxTicksLimit: 5, callback: (v: string | number) => compact(Number(v)) },
      }
      chart = new Chart(canvas.current, {
        type: spec.type,
        data: {
          labels: cats,
          datasets: spec.y.map((f, i) => ({
            label: name(f),
            data: spec.data.map((r) => (typeof r[f] === 'number' ? (r[f] as number) : null)),
            backgroundColor: c.series[i],
            hoverBackgroundColor: c.series[i],
            borderColor: line ? c.series[i] : c.surface,
            hoverBorderColor: line ? c.series[i] : c.surface,
            // 折线 2px；柱子末端 4px 圆角、相邻柱之间留 2px 底色缝
            borderWidth: line ? 2 : horiz ? { top: 1, bottom: 1, left: 0, right: 0 } : { top: 0, right: 1, bottom: 0, left: 1 },
            borderRadius: line ? 0 : horiz ? { topRight: 4, bottomRight: 4 } : { topLeft: 4, topRight: 4 },
            borderSkipped: false,
            maxBarThickness: 28,
            pointRadius: line ? (dense ? 0 : 3) : 0,
            pointHoverRadius: 4,
            pointBackgroundColor: c.series[i],
            pointBorderColor: c.surface,
            pointBorderWidth: 2,
            tension: 0,
            spanGaps: false,
          })),
        },
        options: {
          responsive: true,
          maintainAspectRatio: false,
          animation: false,
          indexAxis: horiz ? 'y' : 'x',
          interaction: { mode: 'index', axis: horiz ? 'y' : 'x', intersect: false },
          layout: { padding: { top: 4, right: horiz ? 8 : 0 } },
          scales: horiz
            ? {
                // 横向：名字最多占四成宽
                y: { grid: { display: false }, border: { color: c.grid }, ticks: { ...catTicks, callback: (_, i) => fit(cats[i], width * 0.4) } },
                x: valAxis,
              }
            : {
                x: {
                  grid: { display: false },
                  border: { color: c.grid },
                  ticks: {
                    ...catTicks,
                    // this 是横轴：宽度随面板拖动变化，每次重排都按当前宽度截
                    callback(_, i) {
                      return keepAll ? fit(cats[i], this.width / cats.length - 6) : cats[i]
                    },
                  },
                },
                y: valAxis,
              },
          plugins: {
            // 一个系列不要图例，标题就是它
            legend: {
              display: spec.y.length > 1,
              position: 'top',
              align: 'start',
              labels: { color: c.text, boxWidth: 8, boxHeight: 8, useBorderRadius: true, borderRadius: 2, font: { size: 11 } },
            },
            tooltip: {
              backgroundColor: c.tipBg,
              titleColor: c.tipText,
              bodyColor: c.tipText,
              borderColor: c.tipBorder,
              borderWidth: 1,
              padding: 8,
              boxWidth: 8,
              boxHeight: 8,
              boxPadding: 4,
              usePointStyle: false,
              callbacks: {
                label: (it) => {
                  const v = horiz ? it.parsed.x : it.parsed.y
                  return ` ${it.dataset.label}：${v == null ? '—' : fmtFull(v, spec.unit)}`
                },
              },
            },
          },
        },
      })
    })
    return () => {
      dead = true
      chart?.destroy()
    }
    // spec 由 src 解析而来，src 不变就是同一份
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [spec, table, theme, horiz, width === 0])

  return (
    <figure ref={box} className={cn('rounded-lg border border-border p-3', full ? 'w-[min(1100px,calc(100vw-2rem))] bg-background shadow-xl' : 'my-2')}>
      <figcaption className="mb-2 flex items-start justify-between gap-2">
        <span className={cn('font-semibold text-foreground', full ? 'text-sm' : 'text-xs')}>
          {spec.title}
          {spec.unit && <span className="ml-1 font-normal text-muted-foreground">（{spec.unit}）</span>}
        </span>
        <span className="-mt-0.5 -mr-1 flex shrink-0 items-center">
          <button
            onClick={() => setTable((t) => !t)}
            aria-pressed={table}
            aria-label={table ? t('看图') : t('看数据')}
            title={table ? t('看图') : t('看数据')}
            className={cn(tool, table && 'bg-accent text-foreground')}
          >
            <Table2 className="size-3.5" />
          </button>
          {!full && (
            <button onClick={() => setZoom(true)} aria-label={t('放大')} title={t('放大')} className={tool}>
              <Maximize2 className="size-3.5" />
            </button>
          )}
        </span>
      </figcaption>
      {table ? (
        <div className={cn('scroll-thin overflow-auto', full ? 'max-h-[65vh]' : 'max-h-64')}>
          <table className="m-0! table! w-full text-xs tabular-nums">
            <thead>
              <tr className="text-muted-foreground">
                <th className="py-1 pr-3 text-left font-medium">{name(spec.x)}</th>
                {spec.y.map((f) => (
                  <th key={f} className="py-1 pl-3 text-right font-medium">
                    {name(f)}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {spec.data.map((r, i) => (
                <tr key={i} className="border-t border-border">
                  <td className="py-1 pr-3">{String(r[spec.x])}</td>
                  {spec.y.map((f) => (
                    <td key={f} className="py-1 pl-3 text-right">
                      {typeof r[f] === 'number' ? fmtFull(r[f] as number, '') : '—'}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="relative" style={{ height }}>
          <canvas ref={canvas} role="img" aria-label={spec.title} />
        </div>
      )}
      {zoom && (
        <Lightbox open onClose={() => setZoom(false)} label={spec.title}>
          <ChartView spec={spec} full />
        </Lightbox>
      )}
    </figure>
  )
}
