import { useEffect, useMemo, useState } from 'react'
import { getJSON, type UsageDay, type UsageReport } from '@/lib/api'
import { fmtTokens } from '@/lib/format'
import { RangeToggle, Skeleton } from '@/components/ui/controls'
import { HoverCard, Legend, Row, niceMax, useWidth } from '@/components/charts'
import { cn } from '@/lib/utils'
import { getLocale, t } from '@/lib/i18n'

const SERIES = [
  { key: 'input', label: '输入', color: 'var(--series-1)' },
  { key: 'cache_read', label: '缓存命中', color: 'var(--series-2)' },
  { key: 'output', label: '输出', color: 'var(--series-3)' },
] as const

const fmtInt = (n: number) => n.toLocaleString(getLocale() === 'en' ? 'en-US' : 'zh-CN')
const WEEK = ['一', '二', '三', '四', '五', '六', '日']
const EN_MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
const EN_WEEK = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun']
/** 月份标签：中文「9月」，英文「Sep」 */
const monthLabel = (m: number) => (getLocale() === 'en' ? EN_MONTHS[m - 1] : `${m}月`)
/** 日期加星期：中文「9月3日 周三」，英文「Sep 3 · Wed」 */
const dayWeek = (d: string) => {
  const m = Number(d.slice(5, 7))
  const day = Number(d.slice(8, 10))
  const w = (new Date(d + 'T00:00:00').getDay() + 6) % 7
  return getLocale() === 'en' ? `${EN_MONTHS[m - 1]} ${day} · ${EN_WEEK[w]}` : `${m}月${day}日 周${WEEK[w]}`
}

/** 用量：本机记录的模型 token 消耗 */
export default function UsagePage() {
  const [days, setDays] = useState('30')
  const [rep, setRep] = useState<UsageReport | null>(null)
  const [err, setErr] = useState('')

  useEffect(() => {
    getJSON<UsageReport>(`usage?days=${days}`)
      .then((r) => {
        setRep(r)
        setErr('')
      })
      .catch((e) => setErr(e.message))
  }, [days])

  return (
    <div className="scroll-thin h-full overflow-y-auto bg-muted/40 p-6">
      <div className="mx-auto w-full max-w-[1200px]">
        <div className="mb-6 flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
          <div className="space-y-2">
            <h3 className="text-base font-semibold tracking-tight">{t('用量')}</h3>
            <p className="text-sm text-muted-foreground">{t('agent 每次调用模型的 token 消耗，只记录在这台电脑的 ~/.annulo/usage.jsonl。')}</p>
          </div>
          <RangeToggle
            value={days}
            onChange={setDays}
            options={[
              { value: '7', label: t('近 {n} 天', { n: 7 }) },
              { value: '30', label: t('近 {n} 天', { n: 30 }) },
              { value: '90', label: t('近 {n} 天', { n: 90 }) },
            ]}
          />
        </div>

        {err && <div className="mb-4 rounded-lg bg-destructive/10 px-3 py-2 text-sm text-destructive">{err}</div>}

        {!rep ? (
          <div className="space-y-4">
            <Skeleton className="h-24" />
            <Skeleton className="h-72" />
            <Skeleton className="h-44" />
          </div>
        ) : rep.totals.requests === 0 && rep.heatmap.every((d) => d.total === 0) ? (
          <div className="rounded-lg border border-dashed border-border bg-muted/40 px-4 py-10 text-center text-sm text-muted-foreground">
            {t('还没有用量记录。和右侧的 agent 对话后，这里会按天、按模型统计 token。')}
          </div>
        ) : (
          <div className="space-y-6">
            <Stats rep={rep} />
            <Section title={t('每日用量')} aside={<Legend items={SERIES.map((s) => ({ label: t(s.label), color: s.color }))} />}>
              <DailyChart data={rep.daily} />
            </Section>
            <Section title={t('最近一年')} aside={<span className="text-xs text-muted-foreground">{t('每格一天，颜色越深 token 越多')}</span>}>
              <Heatmap data={rep.heatmap} />
            </Section>
            <div className="grid gap-6 lg:grid-cols-[1fr_320px]">
              <Section title="模型">
                <Models rep={rep} />
              </Section>
              <Section title={t('活跃时段')} aside={<span className="text-xs text-muted-foreground">{t('按小时')}</span>}>
                <Hours hours={rep.hours} />
              </Section>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

function Section({ title, aside, children }: { title: string; aside?: React.ReactNode; children: React.ReactNode }) {
  return (
    <section className="rounded-xl border border-border bg-background p-4">
      <div className="mb-3 flex items-center justify-between gap-4">
        <h4 className="text-sm font-semibold">{title}</h4>
        {aside}
      </div>
      {children}
    </section>
  )
}

function Stats({ rep }: { rep: UsageReport }) {
  const tot = rep.totals
  const promptSide = tot.input + tot.cache_read + tot.cache_write
  const hit = promptSide > 0 ? tot.cache_read / promptSide : 0
  const active = rep.daily.filter((d) => d.total > 0).length
  const items = [
    { label: t('近 {n} 天', { n: rep.days }), value: fmtTokens(tot.total), sub: `${fmtInt(tot.total)} token` },
    { label: t('今天'), value: fmtTokens(rep.today.total), sub: t('{n} 次请求', { n: rep.today.requests }) },
    { label: t('请求次数'), value: fmtInt(tot.requests), sub: t('{n} 天有使用', { n: active }) },
    { label: t('缓存命中率'), value: `${(hit * 100).toFixed(0)}%`, sub: t('输入中命中缓存的比例') },
    { label: t('日均'), value: fmtTokens(active ? tot.total / active : 0), sub: t('按有使用的天算') },
  ]
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
      {items.map((i) => (
        <div key={i.label} className="rounded-xl border border-border bg-background px-4 py-3">
          <div className="text-xs text-muted-foreground">{i.label}</div>
          <div className="mt-1 text-2xl font-semibold tracking-tight tabular-nums">{i.value}</div>
          <div className="mt-0.5 text-xs text-muted-foreground">{i.sub}</div>
        </div>
      ))}
    </div>
  )
}

function DailyChart({ data }: { data: UsageDay[] }) {
  const [ref, w] = useWidth<HTMLDivElement>()
  const [hover, setHover] = useState<{ i: number; x: number; y: number } | null>(null)
  const H = 240
  const padL = 44
  const padB = 24
  const padT = 8
  const innerW = Math.max(0, w - padL)
  const innerH = H - padB - padT
  const max = niceMax(Math.max(...data.map((d) => d.total)))
  const step = data.length ? innerW / data.length : 0
  const barW = Math.max(2, Math.min(28, step * 0.64))
  const labelEvery = Math.ceil(data.length / Math.max(1, Math.floor(innerW / 64)))
  const y = (v: number) => padT + innerH - (v / max) * innerH

  return (
    <div ref={ref} className="relative" onMouseLeave={() => setHover(null)}>
      {w > 0 && (
        <svg width={w} height={H} role="img" aria-label={t('每日 token 用量')}>
          {[0, 0.25, 0.5, 0.75, 1].map((f) => (
            <g key={f}>
              <line x1={padL} x2={w} y1={y(max * f)} y2={y(max * f)} stroke="var(--border)" strokeDasharray={f === 0 ? undefined : '3 4'} />
              <text x={padL - 8} y={y(max * f) + 4} textAnchor="end" fontSize="11" fill="var(--muted-foreground)" className="tabular-nums">
                {fmtTokens(max * f)}
              </text>
            </g>
          ))}
          {data.map((d, i) => {
            const x = padL + i * step + (step - barW) / 2
            let acc = 0
            const segs = SERIES.map((s) => {
              const v = d[s.key]
              const top = y(acc + v)
              const h = y(acc) - top
              acc += v
              return { ...s, top, h }
            }).filter((s) => s.h > 0)
            return (
              <g key={d.day} opacity={hover && hover.i !== i ? 0.45 : 1}>
                {segs.map((s, j) => (
                  // 段之间留 1px 的缝，最上面一段圆角
                  <rect
                    key={s.key}
                    x={x}
                    y={s.top + (j < segs.length - 1 ? 0 : 0)}
                    width={barW}
                    height={Math.max(0, s.h - (j < segs.length - 1 ? 1 : 0))}
                    rx={j === segs.length - 1 ? Math.min(3, barW / 2) : 0}
                    fill={s.color}
                  />
                ))}
                {i % labelEvery === 0 && (
                  <text x={x + barW / 2} y={H - 6} textAnchor="middle" fontSize="11" fill="var(--muted-foreground)">
                    {d.day.slice(5).replace('-', '/')}
                  </text>
                )}
                {/* 热区比柱子宽，方便悬停 */}
                <rect
                  x={padL + i * step}
                  y={padT}
                  width={step}
                  height={innerH}
                  fill="transparent"
                  onMouseMove={(e) => {
                    const r = (e.currentTarget.ownerSVGElement as SVGSVGElement).getBoundingClientRect()
                    setHover({ i, x: e.clientX - r.left, y: e.clientY - r.top })
                  }}
                />
              </g>
            )
          })}
        </svg>
      )}
      {hover && data[hover.i] && (
        <HoverCard x={hover.x} y={hover.y} containerWidth={w}>
          <div className="mb-1 font-medium">
            {dayWeek(data[hover.i].day)}
          </div>
          {SERIES.map((s) => (
            <Row key={s.key} color={s.color} label={t(s.label)} value={fmtInt(data[hover.i][s.key])} />
          ))}
          <div className="mt-1 border-t pt-1">
            <Row label={t('合计')} value={`${fmtInt(data[hover.i].total)} · ${t('{n} 次', { n: data[hover.i].requests })}`} />
          </div>
        </HoverCard>
      )}
    </div>
  )
}

function Heatmap({ data }: { data: UsageDay[] }) {
  const [ref, w] = useWidth<HTMLDivElement>()
  const [hover, setHover] = useState<{ d: UsageDay; x: number; y: number } | null>(null)
  const weeks = Math.ceil(data.length / 7)
  const labelW = 22
  const gap = 3
  const cell = w ? Math.max(8, Math.min(14, Math.floor((w - labelW) / weeks) - gap)) : 11
  // 按非零天的分位数切 4 档，个别特别高的一天不会把其他天都压成最浅
  const levels = useMemo(() => {
    const v = data.map((d) => d.total).filter((n) => n > 0).sort((a, b) => a - b)
    const q = (p: number) => v[Math.min(v.length - 1, Math.floor(p * v.length))] ?? 0
    return [q(0.25), q(0.5), q(0.75)]
  }, [data])
  const level = (n: number) => (n <= 0 ? 0 : n <= levels[0] ? 1 : n <= levels[1] ? 2 : n <= levels[2] ? 3 : 4)
  const months: { col: number; label: string }[] = []
  for (let c = 0; c < weeks; c++) {
    const d = data[c * 7]
    if (d && (c === 0 || d.day.slice(8) <= '07')) {
      const m = Number(d.day.slice(5, 7))
      // 相邻两个月份标签至少隔 3 列，开头不满一个月的那段不标
      if (!months.length || months[months.length - 1].label !== monthLabel(m)) {
        if (months.length && c - months[months.length - 1].col < 3) months.pop()
        months.push({ col: c, label: monthLabel(m) })
      }
    }
  }
  return (
    <div ref={ref} className="relative" onMouseLeave={() => setHover(null)}>
      <div className="relative mb-1.5 h-4 text-[11px] text-muted-foreground" style={{ marginLeft: labelW }}>
        {months.map((m) => (
          <span key={m.col} className="absolute" style={{ left: m.col * (cell + gap) }}>
            {m.label}
          </span>
        ))}
      </div>
      <div className="flex">
        <div className="grid shrink-0 text-[10px] text-muted-foreground" style={{ width: labelW, gridTemplateRows: `repeat(7, ${cell}px)`, rowGap: gap }}>
          {WEEK.map((d, i) => (
            <span key={d} className="leading-none" style={{ lineHeight: `${cell}px` }}>
              {i % 2 === 0 ? d : ''}
            </span>
          ))}
        </div>
        <div className="grid grid-flow-col" style={{ gridTemplateRows: `repeat(7, ${cell}px)`, gridAutoColumns: `${cell}px`, gap }}>
          {data.map((d) => (
            <div
              key={d.day}
              className="rounded-[3px] transition-[outline] hover:outline-1 hover:outline-foreground/60"
              style={{ background: `var(--heat-${level(d.total)})` }}
              onMouseMove={(e) => {
                const r = ref.current!.getBoundingClientRect()
                setHover({ d, x: e.clientX - r.left, y: e.clientY - r.top })
              }}
            />
          ))}
        </div>
      </div>
      <div className="mt-3 flex items-center justify-end gap-1.5 text-[11px] text-muted-foreground">
        {t('少')}
        {[0, 1, 2, 3, 4].map((l) => (
          <span key={l} className="size-2.5 rounded-[3px]" style={{ background: `var(--heat-${l})` }} />
        ))}
        {t('多')}
      </div>
      {hover && (
        <HoverCard x={hover.x} y={hover.y} containerWidth={w}>
          <div className="mb-1 font-medium">
            {dayWeek(hover.d.day)}
          </div>
          {hover.d.total ? (
            <>
              <Row label="token" value={fmtInt(hover.d.total)} />
              <Row label={t('请求')} value={t('{n} 次', { n: hover.d.requests })} />
            </>
          ) : (
            <div className="text-muted-foreground">{t('没有使用')}</div>
          )}
        </HoverCard>
      )}
    </div>
  )
}

function Models({ rep }: { rep: UsageReport }) {
  if (!rep.models.length) return <p className="text-sm text-muted-foreground">{t('这段时间没有模型调用。')}</p>
  const max = rep.models[0].total
  return (
    <div className="overflow-x-auto rounded-lg border border-border">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-border bg-muted/40 text-left text-xs text-muted-foreground">
            <th className="px-3 py-2 font-medium">{t('模型')}</th>
            <th className="w-20 px-3 py-2 text-right font-medium">{t('请求')}</th>
            <th className="w-20 px-3 py-2 text-right font-medium">{t('输入')}</th>
            <th className="w-24 px-3 py-2 text-right font-medium">{t('缓存命中')}</th>
            <th className="w-20 px-3 py-2 text-right font-medium">{t('输出')}</th>
            <th className="w-24 px-3 py-2 text-right font-medium">{t('合计')}</th>
          </tr>
        </thead>
        <tbody>
          {rep.models.map((m) => (
            <tr key={m.model} className="border-b border-border last:border-b-0">
              <td className="px-3 py-2">
                <div className="font-mono text-[13px]">{m.model}</div>
                <div className="mt-1.5 h-1 rounded-full bg-muted">
                  <div className="h-full rounded-full bg-primary" style={{ width: `${(m.total / max) * 100}%` }} />
                </div>
              </td>
              <td className="px-3 py-2 text-right text-muted-foreground tabular-nums">{fmtInt(m.requests)}</td>
              <td className="px-3 py-2 text-right text-muted-foreground tabular-nums">{fmtTokens(m.input)}</td>
              <td className="px-3 py-2 text-right text-muted-foreground tabular-nums">{fmtTokens(m.cache_read)}</td>
              <td className="px-3 py-2 text-right text-muted-foreground tabular-nums">{fmtTokens(m.output)}</td>
              <td className="px-3 py-2 text-right font-medium tabular-nums">
                {fmtTokens(m.total)}
                <div className="text-xs font-normal text-muted-foreground">{((m.total / rep.totals.total) * 100).toFixed(0)}%</div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function Hours({ hours }: { hours: number[] }) {
  const max = Math.max(1, ...hours)
  const [hover, setHover] = useState<number | null>(null)
  return (
    <div>
      <div className="flex h-28 items-end gap-[3px]" onMouseLeave={() => setHover(null)}>
        {hours.map((v, h) => (
          <div key={h} className="flex h-full flex-1 items-end" onMouseEnter={() => setHover(h)}>
            <div
              className={cn('w-full rounded-t-[3px] transition-opacity', v ? 'bg-primary' : 'bg-muted', hover !== null && hover !== h && 'opacity-45')}
              style={{ height: v ? `${Math.max(4, (v / max) * 100)}%` : 3 }}
            />
          </div>
        ))}
      </div>
      <div className="mt-2 flex justify-between text-[11px] text-muted-foreground tabular-nums">
        <span>{t('0 时')}</span>
        <span>6</span>
        <span>12</span>
        <span>18</span>
        <span>23</span>
      </div>
      <div className="mt-3 h-4 text-xs text-muted-foreground">
        {hover !== null && (
          <span>
            {hover}:00 – {hover}:59 · <span className="font-medium text-foreground tabular-nums">{fmtInt(hours[hover])}</span> token
          </span>
        )}
      </div>
    </div>
  )
}
