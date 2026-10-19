<script>
	import { onMount } from 'svelte'
	import TrafficChart from '../components/TrafficChart.svelte'
	import { formatBytes, formatRate } from '../format.js'
	import { m } from '../paraglide/messages.js'
	import { apiFetch, restore } from '../session.svelte.js'

	// Обзор: что сейчас идёт через сервер и что шло раньше.
	//
	// Живые числа приходят потоком (SSE): панель сама присылает снимок раз в
	// секунду, опрашивать её незачем. История — отдельным запросом: она меняется
	// медленно, и перестраивать весь ряд каждую секунду незачем.

	const ranges = [
		{ value: '1h', label: m.overview_range_hour },
		{ value: '3h', label: m.overview_range_three_hours },
		{ value: '24h', label: m.overview_range_day },
		{ value: '7d', label: m.overview_range_week }
	]

	let live = $state(null)
	let liveLost = $state(false)

	let range = $state('1h')
	let history = $state(null)
	let historyLoading = $state(true)
	let historyFailed = $state(false)

	const totals = $derived(live?.total ?? { rx: 0, tx: 0, rx_rate: 0, tx_rate: 0 })
	const users = $derived(live?.users ?? [])
	const online = $derived(users.filter((user) => user.online).length)
	const step = $derived(history?.step_seconds ?? 0)
	const points = $derived(history?.points ?? [])

	onMount(() => {
		loadHistory()

		const stream = new EventSource('/api/events')
		stream.addEventListener('metrics', (event) => {
			live = JSON.parse(event.data)
			liveLost = false
		})

		stream.onerror = () => {
			// Пока панель отвечает, EventSource переподключается сам. Если он
			// закрылся совсем — вход кончился: спрашиваем сессию заново.
			liveLost = true
			if (stream.readyState === EventSource.CLOSED) {
				restore()
			}
		}

		// История перечитывается реже живых чисел: последние секунды всё равно
		// видны в таблице, а график отставания в полминуты не замечает.
		const refresh = setInterval(loadHistory, 30_000)

		return () => {
			stream.close()
			clearInterval(refresh)
		}
	})

	async function loadHistory() {
		const wanted = range
		historyFailed = false

		try {
			const answer = await apiFetch('/api/metrics/history?range=' + wanted)
			if (!answer.ok) {
				historyFailed = true
				return
			}

			const received = await answer.json()

			// Пока ответ шёл, отрезок могли переключить: чужой ряд показывать
			// нельзя, иначе подписи не сойдутся с данными.
			if (received.range === range) {
				history = received
			}
		} catch {
			historyFailed = true
		} finally {
			if (wanted === range) {
				historyLoading = false
			}
		}
	}

	function choose(next) {
		if (next === range) {
			return
		}

		range = next
		historyLoading = true
		loadHistory()
	}
</script>

<h1 class="text-xl font-semibold">{m.nav_overview()}</h1>
<p class="mt-3 text-slate-400">{m.overview_hint()}</p>

{#if liveLost}
	<p class="mt-4 text-sm text-amber-400" role="status">{m.overview_stream_lost()}</p>
{/if}

<section class="mt-6 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
	<div class="rounded border border-slate-800 bg-slate-900/40 p-4">
		<p class="text-xs uppercase tracking-wide text-slate-500">{m.overview_users()}</p>
		<p class="mt-2 text-2xl font-semibold tabular-nums">{users.length}</p>
	</div>

	<div class="rounded border border-slate-800 bg-slate-900/40 p-4">
		<p class="text-xs uppercase tracking-wide text-slate-500">{m.overview_online()}</p>
		<p class="mt-2 text-2xl font-semibold tabular-nums text-sky-300">{online}</p>
	</div>

	<div class="rounded border border-slate-800 bg-slate-900/40 p-4">
		<p class="text-xs uppercase tracking-wide text-slate-500">{m.overview_rx()}</p>
		<p class="mt-2 text-2xl font-semibold tabular-nums text-sky-300">
			{formatRate(totals.rx_rate)}
		</p>
		<p class="mt-1 text-xs text-slate-500">{formatBytes(totals.rx)}</p>
	</div>

	<div class="rounded border border-slate-800 bg-slate-900/40 p-4">
		<p class="text-xs uppercase tracking-wide text-slate-500">{m.overview_tx()}</p>
		<p class="mt-2 text-2xl font-semibold tabular-nums text-emerald-300">
			{formatRate(totals.tx_rate)}
		</p>
		<p class="mt-1 text-xs text-slate-500">{formatBytes(totals.tx)}</p>
	</div>
</section>

<section class="mt-6 rounded border border-slate-800 bg-slate-900/40 p-6">
	<div class="flex flex-wrap items-center gap-x-4 gap-y-3">
		<h2 class="font-semibold">{m.overview_chart_title()}</h2>

		<div class="ml-auto flex flex-wrap gap-1">
			{#each ranges as item (item.value)}
				<button
					type="button"
					onclick={() => choose(item.value)}
					class="rounded px-3 py-1.5 text-sm transition-colors {range === item.value
						? 'bg-slate-800 text-slate-100'
						: 'text-slate-400 hover:text-white'}"
				>
					{item.label()}
				</button>
			{/each}
		</div>
	</div>

	<div class="mt-4">
		{#if historyLoading && !history}
			<p class="py-12 text-center text-sm text-slate-400">{m.overview_chart_loading()}</p>
		{:else if historyFailed}
			<p class="py-12 text-center text-sm text-rose-400" role="alert">
				{m.overview_chart_failed()}
			</p>
		{:else}
			<TrafficChart {points} {step} {range} />
		{/if}
	</div>
</section>

<section class="mt-6 rounded border border-slate-800 bg-slate-900/40">
	{#if !live}
		<p class="p-6 text-sm text-slate-400">{m.overview_waiting()}</p>
	{:else if users.length === 0}
		<p class="p-6 text-sm text-slate-400">{m.overview_no_users()}</p>
	{:else}
		<div class="overflow-x-auto">
			<table class="w-full text-sm">
				<thead class="text-left text-xs uppercase tracking-wide text-slate-500">
					<tr class="border-b border-slate-800">
						<th class="px-6 py-3 font-medium">{m.overview_table_user()}</th>
						<th class="px-6 py-3 font-medium">{m.overview_table_state()}</th>
						<th class="px-6 py-3 text-right font-medium">{m.overview_table_rx()}</th>
						<th class="px-6 py-3 text-right font-medium">{m.overview_table_tx()}</th>
					</tr>
				</thead>
				<tbody>
					{#each users as user (user.id)}
						<tr class="border-b border-slate-800 last:border-b-0">
							<td class="px-6 py-3">
								<span class="font-medium text-slate-100">{user.name}</span>
								{#if user.online}
									<span class="ml-2 text-xs text-sky-300">{m.users_online()}</span>
								{/if}
							</td>
							<td class="px-6 py-3">
								<span
									class="rounded px-2 py-0.5 text-xs {user.enabled
										? 'bg-emerald-950 text-emerald-300'
										: 'bg-slate-800 text-slate-400'}"
								>
									{user.enabled ? m.users_enabled() : m.users_disabled()}
								</span>
							</td>
							<td class="px-6 py-3 text-right tabular-nums text-sky-300">
								<div>{formatRate(user.rx_rate)}</div>
								<div class="text-xs text-slate-500">{formatBytes(user.rx)}</div>
							</td>
							<td class="px-6 py-3 text-right tabular-nums text-emerald-300">
								<div>{formatRate(user.tx_rate)}</div>
								<div class="text-xs text-slate-500">{formatBytes(user.tx)}</div>
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</section>
