<script>
	import { formatClock, formatRate } from '../format.js'
	import { m } from '../paraglide/messages.js'

	// График трафика сервера.
	//
	// Рисуется своим SVG, без библиотеки: нужны две линии и три подписи, а
	// библиотека принесла бы сотни килобайт и своё представление о мире. Окна
	// истории показывают байты за шаг, а на графике — скорость: иначе окно в
	// пять минут выглядело бы в шестьдесят раз тяжелее пятисекундного.

	let { points = [], step = 5, range = '1h' } = $props()

	const width = 720
	const height = 260
	const padding = { top: 16, right: 16, bottom: 30, left: 76 }
	const plotWidth = width - padding.left - padding.right
	const plotHeight = height - padding.top - padding.bottom

	const series = $derived(
		points.map((point) => ({
			at: point.at,
			rx: step > 0 ? point.rx / step : 0,
			tx: step > 0 ? point.tx / step : 0
		}))
	)

	const empty = $derived(series.every((point) => point.rx === 0 && point.tx === 0))

	// Верхняя граница не может быть нулём: на ноль делить нельзя, а плоская
	// линия по нижнему краю читается лучше, чем пустое поле.
	const peak = $derived(
		Math.max(1, ...series.flatMap((point) => [point.rx, point.tx]))
	)

	function x(index) {
		if (series.length <= 1) {
			return padding.left + plotWidth
		}

		return padding.left + (index / (series.length - 1)) * plotWidth
	}

	function y(value) {
		return padding.top + plotHeight * (1 - value / peak)
	}

	function line(key) {
		return series
			.map((point, index) => `${x(index).toFixed(1)},${y(point[key]).toFixed(1)}`)
			.join(' ')
	}

	const ticks = $derived([peak, peak / 2, 0])

	// Подписи оси времени: начало, середина и конец ряда. Больше на узком экране
	// не помещается, а точное время всегда видно по живому значению сверху.
	const labels = $derived.by(() => {
		if (series.length === 0) {
			return []
		}

		const withDate = range === '24h' || range === '7d'
		const indexes = [0, Math.floor((series.length - 1) / 2), series.length - 1]

		return indexes.map((index) => ({
			x: x(index),
			text: formatClock(series[index].at, withDate)
		}))
	})
</script>

<div class="flex flex-wrap items-center gap-x-5 gap-y-2 text-xs text-slate-400">
	<span class="flex items-center gap-1.5">
		<span class="h-0.5 w-4 rounded bg-sky-400"></span>
		{m.overview_rx()}
	</span>
	<span class="flex items-center gap-1.5">
		<span class="h-0.5 w-4 rounded bg-emerald-400"></span>
		{m.overview_tx()}
	</span>
	<span class="ml-auto">{m.overview_chart_step({ step })}</span>
</div>

<div class="relative mt-3">
	<svg
		viewBox={`0 0 ${width} ${height}`}
		class="w-full"
		role="img"
		aria-label={m.overview_chart_title()}
	>
		{#each ticks as tick}
			<line
				x1={padding.left}
				x2={width - padding.right}
				y1={y(tick)}
				y2={y(tick)}
				class="stroke-slate-800"
				stroke-width="1"
			/>
			<text
				x={padding.left - 10}
				y={y(tick) + 4}
				text-anchor="end"
				class="fill-slate-500 text-[10px]">{formatRate(tick)}</text
			>
		{/each}

		{#each labels as label}
			<text
				x={label.x}
				y={height - 8}
				text-anchor="middle"
				class="fill-slate-500 text-[10px]">{label.text}</text
			>
		{/each}

		<polyline
			points={line('rx')}
			fill="none"
			class="stroke-sky-400"
			stroke-width="2"
			stroke-linejoin="round"
			stroke-linecap="round"
		/>
		<polyline
			points={line('tx')}
			fill="none"
			class="stroke-emerald-400"
			stroke-width="2"
			stroke-linejoin="round"
			stroke-linecap="round"
		/>
	</svg>

	{#if empty}
		<p class="absolute inset-0 flex items-center justify-center text-sm text-slate-500">
			{m.overview_chart_empty()}
		</p>
	{/if}
</div>
