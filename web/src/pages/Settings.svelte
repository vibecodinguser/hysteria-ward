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

	// Домен сервера: по нему клиенты видят сервер, и он же идёт в ключи. Живёт
	// рядом с настройками панели, а не в конфигурации ядра: смена домена не
	// повод перезапускать сервер.
	let domain = $state('')
	let serverLoading = $state(true)
	let serverLoaded = $state(false)
	let serverFailure = $state(null)
	let serverSaving = $state(false)
	let serverSaved = $state(false)

	// Masquerade: подставной ответ посторонним запросам. Это часть конфигурации
	// ядра, поэтому правка вступает в силу только по «Применить».
	let masquerade = $state({ mode: 'off', file: '', proxy: '' })
	let masqueradeApplying = $state(false)
	let masqueradeApplied = $state(false)
	let masqueradeFailure = $state(null)

	load()
	loadServer()

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

	async function loadServer() {
		try {
			const answer = await apiFetch('/api/server')
			if (!answer.ok) {
				serverFailure = m.settings_server_failed()
				return
			}

			const settings = await answer.json()
			domain = settings.domain
			masquerade = settings.masquerade
			serverLoaded = true
		} catch {
			serverFailure = m.settings_server_failed()
		} finally {
			serverLoading = false
		}
	}

	async function saveServer() {
		// Двойной щелчок не должен превращаться в две записи.
		if (serverSaving) {
			return
		}

		serverSaving = true
		serverFailure = null
		serverSaved = false

		try {
			const answer = await apiFetch('/api/server', {
				method: 'PUT',
				body: { domain }
			})

			if (!answer.ok) {
				// Причину называет панель: она знает, чем имя не годится.
				serverFailure = (await reasonOf(answer)) ?? m.settings_server_failed()
				return
			}

			// Панель возвращает записанное: в базе домен лежит уже приведённым к
			// нижнему регистру, и показывать введённое как есть — обманывать.
			domain = (await answer.json()).domain
			serverSaved = true
		} catch {
			serverFailure = m.session_unreachable()
		} finally {
			serverSaving = false
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

	async function applyMasquerade() {
		// Двойной щелчок не должен перезапускать ядро дважды.
		if (masqueradeApplying) {
			return
		}

		masqueradeApplying = true
		masqueradeFailure = null
		masqueradeApplied = false

		try {
			const answer = await apiFetch('/api/server/apply', {
				method: 'POST',
				body: { masquerade }
			})

			if (!answer.ok) {
				// Причину называет панель: только она знает, нашёлся ли каталог и
				// поднялось ли ядро.
				masqueradeFailure = (await reasonOf(answer)) ?? m.settings_masquerade_failed()
				return
			}

			// Панель возвращает записанное: показывать введённое как есть, когда
			// база хранит уже другое, — обманывать.
			masquerade = (await answer.json()).masquerade
			masqueradeApplied = true
		} catch {
			masqueradeFailure = m.session_unreachable()
		} finally {
			masqueradeApplying = false
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

<section class="mt-6 rounded border border-slate-800 bg-slate-900/40 p-6">
	<h2 class="font-semibold">{m.settings_server_title()}</h2>

	{#if serverLoading}
		<p class="mt-2 text-sm text-slate-400">{m.settings_panel_loading()}</p>
	{:else if serverLoaded}
		<p class="mt-2 text-sm text-slate-400">{m.settings_server_hint()}</p>

		{#if domain === ''}
			<p class="mt-4 text-sm text-amber-400" role="status">{m.settings_server_empty()}</p>
		{/if}

		<form
			class="mt-4 flex flex-wrap gap-3"
			onsubmit={(event) => {
				event.preventDefault()
				saveServer()
			}}
		>
			<input
				type="text"
				bind:value={domain}
				placeholder={m.settings_server_placeholder()}
				aria-label={m.settings_server_domain()}
				autocomplete="off"
				class="min-w-56 flex-1 rounded border border-slate-700 bg-slate-950 px-3 py-2 text-sm text-slate-100 placeholder:text-slate-600"
			/>
			<button
				type="submit"
				disabled={serverSaving}
				class="rounded bg-slate-100 px-3 py-2 text-sm font-medium text-slate-900 transition-colors hover:bg-white disabled:opacity-60"
			>
				{serverSaving ? m.settings_server_saving() : m.settings_server_save()}
			</button>
		</form>

		{#if serverSaved}
			<p class="mt-3 text-sm text-emerald-400" role="status">{m.settings_server_saved()}</p>
		{/if}
		{#if serverFailure}
			<p class="mt-3 text-sm text-rose-400" role="alert">{serverFailure}</p>
		{/if}

		<div class="mt-6 border-t border-slate-800 pt-6">
			<h3 class="font-semibold">{m.settings_masquerade_title()}</h3>
			<p class="mt-2 text-sm text-slate-400">{m.settings_masquerade_hint()}</p>

			<form
				class="mt-4 flex flex-wrap items-end gap-3"
				onsubmit={(event) => {
					event.preventDefault()
					applyMasquerade()
				}}
			>
				<label class="flex flex-col gap-1 text-sm text-slate-300">
					<span>{m.settings_masquerade_mode()}</span>
					<select
						bind:value={masquerade.mode}
						class="rounded border border-slate-700 bg-slate-950 px-3 py-2 text-sm text-slate-100"
					>
						<option value="off">{m.settings_masquerade_off()}</option>
						<option value="file">{m.settings_masquerade_file()}</option>
						<option value="proxy">{m.settings_masquerade_proxy()}</option>
					</select>
				</label>

				{#if masquerade.mode === 'file'}
					<label class="flex min-w-56 flex-1 flex-col gap-1 text-sm text-slate-300">
						<span>{m.settings_masquerade_file_path()}</span>
						<input
							type="text"
							bind:value={masquerade.file}
							placeholder={m.settings_masquerade_file_placeholder()}
							autocomplete="off"
							class="rounded border border-slate-700 bg-slate-950 px-3 py-2 text-sm text-slate-100 placeholder:text-slate-600"
						/>
					</label>
				{:else if masquerade.mode === 'proxy'}
					<label class="flex min-w-56 flex-1 flex-col gap-1 text-sm text-slate-300">
						<span>{m.settings_masquerade_proxy_url()}</span>
						<input
							type="url"
							bind:value={masquerade.proxy}
							placeholder={m.settings_masquerade_proxy_placeholder()}
							autocomplete="off"
							class="rounded border border-slate-700 bg-slate-950 px-3 py-2 text-sm text-slate-100 placeholder:text-slate-600"
						/>
					</label>
				{/if}

				<button
					type="submit"
					disabled={masqueradeApplying}
					class="rounded bg-slate-100 px-3 py-2 text-sm font-medium text-slate-900 transition-colors hover:bg-white disabled:opacity-60"
				>
					{masqueradeApplying ? m.settings_masquerade_applying() : m.settings_masquerade_apply()}
				</button>
			</form>

			<p class="mt-3 text-xs text-amber-400/80">{m.settings_masquerade_restart()}</p>

			{#if masqueradeApplied}
				<p class="mt-3 text-sm text-emerald-400" role="status">{m.settings_masquerade_applied()}</p>
			{/if}
			{#if masqueradeFailure}
				<p class="mt-3 text-sm text-rose-400" role="alert">{masqueradeFailure}</p>
			{/if}
		</div>
	{:else}
		<p class="mt-2 text-sm text-rose-400" role="alert">
			{serverFailure ?? m.settings_server_failed()}
		</p>
	{/if}
</section>
