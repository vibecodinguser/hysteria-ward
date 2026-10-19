<script>
	import { m } from '../paraglide/messages.js'
	import { apiFetch } from '../session.svelte.js'

	// Доступ к панели: спрятана на петле или видна всем. Состояние спрашивается у
	// панели, а не запоминается здесь: настройка живёт в базе и переживает
	// перезапуск, а страница могла быть открыта и до переключения.
	let panel = $state(null)
	let failure = $state(null)
	let loading = $state(true)
	let sending = $state(false)
	let confirming = $state(false)

	load()

	async function load() {
		try {
			const answer = await apiFetch('/api/panel')
			if (!answer.ok) {
				failure = m.settings_panel_failed()
				return
			}

			panel = await answer.json()
		} catch {
			failure = m.settings_panel_failed()
		} finally {
			loading = false
		}
	}

	async function apply(access) {
		// Двойной щелчок не должен превращаться в две команды: панель перевешивала
		// бы слушатель дважды подряд.
		if (sending) {
			return
		}

		sending = true
		failure = null

		try {
			const answer = await apiFetch('/api/panel/access', {
				method: 'POST',
				body: { access }
			})

			if (!answer.ok) {
				// Причину называет панель: «сначала смените пароль по умолчанию» —
				// это её знание, а не догадка интерфейса.
				failure = (await reasonOf(answer)) ?? m.settings_panel_failed()
				return
			}

			panel = await answer.json()
			confirming = false
		} catch {
			failure = m.session_unreachable()
		} finally {
			sending = false
		}
	}

	async function reasonOf(answer) {
		try {
			const { error } = await answer.json()

			return error || null
		} catch {
			return null
		}
	}
</script>

<h1 class="text-xl font-semibold">{m.nav_settings()}</h1>
<p class="mt-3 text-slate-400">{m.settings_hint()}</p>

<section class="mt-8 rounded border border-slate-800 bg-slate-900/40 p-6">
	<h2 class="font-semibold">{m.settings_panel_title()}</h2>

	{#if loading}
		<p class="mt-2 text-sm text-slate-400">{m.settings_panel_loading()}</p>
	{:else if panel}
		<p class="mt-2 text-sm text-slate-400">
			{m.settings_panel_listening({ addr: panel.addr })}
		</p>

		{#if panel.access === 'public'}
			<p class="mt-4 text-sm text-amber-400" role="status">{m.settings_panel_public()}</p>

			<button
				type="button"
				disabled={sending}
				onclick={() => apply('tunnel')}
				class="mt-4 rounded bg-slate-100 px-3 py-2 text-sm font-medium text-slate-900 transition-colors hover:bg-white disabled:opacity-60"
			>
				{m.settings_panel_close()}
			</button>
		{:else}
			<p class="mt-4 text-sm text-slate-400">{m.settings_panel_hint()}</p>

			{#if confirming}
				<!-- Предупреждение показывается до команды, а не после: включать
				     публичный доступ «на всякий случай» администратор не должен. -->
				<div class="mt-4 rounded border border-amber-700/50 bg-amber-950/30 p-4">
					<p class="text-sm text-amber-300" role="alert">{m.settings_panel_warning()}</p>

					<div class="mt-4 flex flex-wrap gap-3">
						<button
							type="button"
							disabled={sending}
							onclick={() => apply('public')}
							class="rounded bg-amber-400 px-3 py-2 text-sm font-medium text-slate-950 transition-colors hover:bg-amber-300 disabled:opacity-60"
						>
							{m.settings_panel_confirm()}
						</button>
						<button
							type="button"
							onclick={() => (confirming = false)}
							class="rounded px-3 py-2 text-sm text-slate-300 transition-colors hover:text-white"
						>
							{m.settings_panel_cancel()}
						</button>
					</div>
				</div>
			{:else}
				<button
					type="button"
					disabled={sending}
					onclick={() => (confirming = true)}
					class="mt-4 rounded border border-slate-700 px-3 py-2 text-sm text-slate-200 transition-colors hover:border-slate-500 hover:text-white disabled:opacity-60"
				>
					{m.settings_panel_open()}
				</button>
			{/if}
		{/if}

		{#if failure}
			<p class="mt-4 text-sm text-rose-400" role="alert">{failure}</p>
		{/if}
	{:else if failure}
		<p class="mt-2 text-sm text-rose-400" role="alert">{failure}</p>
	{/if}
</section>
