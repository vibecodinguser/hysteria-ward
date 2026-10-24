<script>
	import { formatDateTime, fromMbps, toMbps } from '../format.js'
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

	// Глобальные пределы скорости: в интерфейсе — Мбит/с, в базе и ядре — байты
	// в секунду. Это часть конфигурации ядра, поэтому тоже только по «Применить».
	let bandwidth = $state({ up: 0, down: 0 })
	let bandwidthApplying = $state(false)
	let bandwidthApplied = $state(false)
	let bandwidthFailure = $state(null)

	// Сертификат: состояние выпуска и путь повторить, если ACME не ответил.
	// Панель выпускает его в фоне, поэтому страница только показывает, что вышло.
	let certificate = $state(null)
	let certificateLoading = $state(true)
	let certificateFailure = $state(null)
	let certificateSending = $state(false)
	let certificateRequested = $state(false)

	// MCP-ключи: отдельный вход для агентов. Токен показывается один раз, поэтому
	// после выпуска он живёт только в этой переменной, пока администратор не
	// закроет подсказку.
	let mcpKeys = $state([])
	let mcpLoading = $state(true)
	let mcpFailure = $state(null)
	let mcpName = $state('')
	let mcpCreating = $state(false)
	let mcpNewToken = $state(null)
	let mcpNewName = $state('')
	let mcpNewID = $state(null)
	let mcpCopied = $state(false)
	let mcpConfirming = $state(null)
	let mcpRevoking = $state(null)

	load()
	loadServer()
	loadCertificate()
	loadMCPKeys()

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
			bandwidth = {
				up: toMbps(settings.bandwidth?.up),
				down: toMbps(settings.bandwidth?.down)
			}
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

	async function loadCertificate() {
		try {
			const answer = await apiFetch('/api/certificate')
			if (!answer.ok) {
				certificateFailure = m.settings_certificate_failed()
				return
			}

			certificate = await answer.json()
		} catch {
			certificateFailure = m.settings_certificate_failed()
		} finally {
			certificateLoading = false
		}
	}

	async function retryCertificate() {
		// Двойной щелчок не должен слать две команды: выпуск и так один.
		if (certificateSending) {
			return
		}

		certificateSending = true
		certificateFailure = null
		certificateRequested = false

		try {
			const answer = await apiFetch('/api/certificate/issue', { method: 'POST' })
			if (!answer.ok) {
				certificateFailure = (await reasonOf(answer)) ?? m.settings_certificate_retry_failed()
				return
			}

			certificate = await answer.json()
			certificateRequested = true
		} catch {
			certificateFailure = m.session_unreachable()
		} finally {
			certificateSending = false
		}
	}

	async function loadMCPKeys() {
		try {
			const answer = await apiFetch('/api/mcp/keys')
			if (!answer.ok) {
				mcpFailure = m.settings_mcp_load_failed()
				return
			}

			mcpKeys = (await answer.json()).keys
		} catch {
			mcpFailure = m.settings_mcp_load_failed()
		} finally {
			mcpLoading = false
		}
	}

	async function createMCPKey() {
		// Двойной щелчок не должен выпускать два ключа с одним именем: второй
		// всё равно получит отказ, но лишний запрос ни к чему.
		if (mcpCreating || mcpName.trim() === '') {
			return
		}

		mcpCreating = true
		mcpFailure = null
		mcpCopied = false

		try {
			const answer = await apiFetch('/api/mcp/keys', {
				method: 'POST',
				body: { name: mcpName }
			})

			if (!answer.ok) {
				mcpFailure = (await reasonOf(answer)) ?? m.settings_mcp_create_failed()
				return
			}

			const created = await answer.json()
			mcpKeys = [created, ...mcpKeys]
			mcpNewToken = created.token
			mcpNewName = created.name
			mcpNewID = created.id
			mcpName = ''
		} catch {
			mcpFailure = m.session_unreachable()
		} finally {
			mcpCreating = false
		}
	}

	async function revokeMCPKey(id) {
		if (mcpRevoking !== null) {
			return
		}

		mcpRevoking = id
		mcpFailure = null

		try {
			const answer = await apiFetch('/api/mcp/keys/' + encodeURIComponent(id), {
				method: 'DELETE'
			})
			if (!answer.ok) {
				mcpFailure = (await reasonOf(answer)) ?? m.settings_mcp_revoke_failed()
				return
			}

			mcpKeys = mcpKeys.filter((key) => key.id !== id)
			mcpConfirming = null
			if (mcpNewID === id) {
				mcpNewToken = null
				mcpNewID = null
			}
		} catch {
			mcpFailure = m.session_unreachable()
		} finally {
			mcpRevoking = null
		}
	}

	async function copyMCPToken() {
		if (!mcpNewToken) {
			return
		}

		try {
			await navigator.clipboard.writeText(mcpNewToken)
			mcpCopied = true
		} catch {
			mcpFailure = m.settings_mcp_copy_failed()
		}
	}

	function moment(value) {
		return formatDateTime(value)
	}

	function known(value) {
		return Boolean(value) && !value.startsWith('0001-')
	}

	// В базе способ проверки хранится коротко, а администратору привычнее имя
	// проверки ACME: http — это HTTP-01, dns — DNS-01.
	function modeText(mode) {
		return mode === 'dns' ? 'DNS-01' : 'HTTP-01'
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

	async function applyBandwidth() {
		// Двойной щелчок не должен перезапускать ядро дважды.
		if (bandwidthApplying) {
			return
		}

		bandwidthApplying = true
		bandwidthFailure = null
		bandwidthApplied = false

		try {
			const answer = await apiFetch('/api/server/apply', {
				method: 'POST',
				body: {
					bandwidth: {
						up: fromMbps(bandwidth.up),
						down: fromMbps(bandwidth.down)
					}
				}
			})

			if (!answer.ok) {
				bandwidthFailure = (await reasonOf(answer)) ?? m.settings_bandwidth_failed()
				return
			}

			const settings = await answer.json()
			bandwidth = {
				up: toMbps(settings.bandwidth.up),
				down: toMbps(settings.bandwidth.down)
			}
			bandwidthApplied = true
		} catch {
			bandwidthFailure = m.session_unreachable()
		} finally {
			bandwidthApplying = false
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

		{#if panel.host}
			<p class="mt-1 text-sm text-slate-400">
				{m.settings_panel_host({ host: panel.host })}
			</p>
			{#if panel.access === 'public'}
				<a
					class="mt-2 inline-block text-sm text-sky-400 transition-colors hover:text-sky-300"
					href={`https://${panel.host}:8443`}
					target="_blank"
					rel="noreferrer"
				>
					{m.settings_panel_host_link({ host: panel.host })}
				</a>
			{/if}
		{/if}

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
	<h2 class="font-semibold">{m.settings_certificate_title()}</h2>
	<p class="mt-2 text-sm text-slate-400">{m.settings_certificate_hint()}</p>

	{#if certificateLoading}
		<p class="mt-2 text-sm text-slate-400">{m.settings_certificate_loading()}</p>
	{:else if certificate && !certificate.managed}
		<p class="mt-2 text-sm text-slate-400">{m.settings_certificate_unmanaged()}</p>
	{:else if certificate}
		<p class="mt-4 text-sm text-slate-400">
			{m.settings_certificate_mode({ mode: modeText(certificate.mode) })}
		</p>
		{#if certificate.names.length > 0}
			<p class="mt-1 text-sm text-slate-400">
				{m.settings_certificate_names({ names: certificate.names.join(', ') })}
			</p>
		{/if}

		{#if certificate.ready}
			<p class="mt-3 text-sm text-emerald-400" role="status">
				{m.settings_certificate_ready({ date: moment(certificate.not_after) })}
			</p>
		{:else if certificate.placeholder}
			<p class="mt-3 text-sm text-amber-400" role="status">
				{m.settings_certificate_placeholder()}
			</p>
		{/if}

		{#if certificate.last_error}
			<p class="mt-3 text-sm text-rose-400" role="alert">
				{m.settings_certificate_error({ error: certificate.last_error })}
			</p>
		{/if}
		{#if known(certificate.next_attempt)}
			<p class="mt-2 text-xs text-slate-500">
				{m.settings_certificate_next({ date: moment(certificate.next_attempt) })}
			</p>
		{/if}

		{#if certificate.placeholder || certificate.last_error}
			<button
				type="button"
				disabled={certificateSending}
				onclick={retryCertificate}
				class="mt-4 rounded border border-slate-700 px-3 py-2 text-sm text-slate-200 transition-colors hover:border-slate-500 hover:text-white disabled:opacity-60"
			>
				{certificateSending ? m.settings_certificate_retrying() : m.settings_certificate_retry()}
			</button>
		{/if}

		{#if certificateRequested}
			<p class="mt-3 text-sm text-emerald-400" role="status">{m.settings_certificate_requested()}</p>
		{/if}
		{#if certificateFailure}
			<p class="mt-3 text-sm text-rose-400" role="alert">{certificateFailure}</p>
		{/if}
	{:else if certificateFailure}
		<p class="mt-2 text-sm text-rose-400" role="alert">{certificateFailure}</p>
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
			<h3 class="font-semibold">{m.settings_bandwidth_title()}</h3>
			<p class="mt-2 text-sm text-slate-400">{m.settings_bandwidth_hint()}</p>

			<form
				class="mt-4 flex flex-wrap items-end gap-3"
				onsubmit={(event) => {
					event.preventDefault()
					applyBandwidth()
				}}
			>
				<label class="flex flex-col gap-1 text-sm text-slate-300">
					<span>{m.settings_bandwidth_up()}</span>
					<input
						type="number"
						min="0"
						step="0.1"
						bind:value={bandwidth.up}
						class="w-36 rounded border border-slate-700 bg-slate-950 px-3 py-2 text-sm text-slate-100"
					/>
				</label>

				<label class="flex flex-col gap-1 text-sm text-slate-300">
					<span>{m.settings_bandwidth_down()}</span>
					<input
						type="number"
						min="0"
						step="0.1"
						bind:value={bandwidth.down}
						class="w-36 rounded border border-slate-700 bg-slate-950 px-3 py-2 text-sm text-slate-100"
					/>
				</label>

				<button
					type="submit"
					disabled={bandwidthApplying}
					class="rounded bg-slate-100 px-3 py-2 text-sm font-medium text-slate-900 transition-colors hover:bg-white disabled:opacity-60"
				>
					{bandwidthApplying ? m.settings_bandwidth_applying() : m.settings_bandwidth_apply()}
				</button>
			</form>

			<p class="mt-3 text-xs text-amber-400/80">{m.settings_bandwidth_restart()}</p>

			{#if bandwidthApplied}
				<p class="mt-3 text-sm text-emerald-400" role="status">{m.settings_bandwidth_applied()}</p>
			{/if}
			{#if bandwidthFailure}
				<p class="mt-3 text-sm text-rose-400" role="alert">{bandwidthFailure}</p>
			{/if}
		</div>

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

<section class="mt-6 rounded border border-slate-800 bg-slate-900/40 p-6">
	<h2 class="font-semibold">{m.settings_mcp_title()}</h2>
	<p class="mt-2 text-sm text-slate-400">{m.settings_mcp_hint()}</p>
	<p class="mt-2 text-xs text-amber-400/80">{m.settings_mcp_restriction()}</p>

	{#if mcpLoading}
		<p class="mt-2 text-sm text-slate-400">{m.settings_mcp_loading()}</p>
	{:else}
		{#if mcpNewToken}
			<div class="mt-4 rounded border border-emerald-700/50 bg-emerald-950/30 p-4">
				<p class="text-sm text-emerald-300" role="status">
					{m.settings_mcp_created({ name: mcpNewName })}
				</p>
				<code class="mt-3 block break-all rounded bg-slate-950 px-3 py-2 font-mono text-sm text-slate-100">
					{mcpNewToken}
				</code>

				<div class="mt-3 flex flex-wrap gap-3">
					<button
						type="button"
						onclick={copyMCPToken}
						class="rounded bg-slate-100 px-3 py-2 text-sm font-medium text-slate-900 transition-colors hover:bg-white"
					>
						{mcpCopied ? m.settings_mcp_copied() : m.settings_mcp_copy()}
					</button>
					<button
						type="button"
						onclick={() => {
							mcpNewToken = null
							mcpNewID = null
						}}
						class="rounded px-3 py-2 text-sm text-slate-300 transition-colors hover:text-white"
					>
						{m.settings_mcp_hide()}
					</button>
				</div>

				<p class="mt-3 text-xs text-amber-400/80">{m.settings_mcp_token_hint()}</p>
			</div>
		{/if}

		<form
			class="mt-4 flex flex-wrap gap-3"
			onsubmit={(event) => {
				event.preventDefault()
				createMCPKey()
			}}
		>
			<input
				type="text"
				bind:value={mcpName}
				maxlength="64"
				placeholder={m.settings_mcp_name_placeholder()}
				aria-label={m.settings_mcp_name()}
				autocomplete="off"
				class="min-w-56 flex-1 rounded border border-slate-700 bg-slate-950 px-3 py-2 text-sm text-slate-100 placeholder:text-slate-600"
			/>
			<button
				type="submit"
				disabled={mcpCreating || mcpName.trim() === ''}
				class="rounded bg-slate-100 px-3 py-2 text-sm font-medium text-slate-900 transition-colors hover:bg-white disabled:opacity-60"
			>
				{mcpCreating ? m.settings_mcp_creating() : m.settings_mcp_create()}
			</button>
		</form>

		{#if mcpKeys.length === 0}
			<p class="mt-4 text-sm text-slate-500">{m.settings_mcp_empty()}</p>
		{:else}
			<ul class="mt-4 divide-y divide-slate-800 border-t border-slate-800">
				{#each mcpKeys as key (key.id)}
					<li class="flex flex-wrap items-center justify-between gap-3 py-3">
						<div>
							<p class="text-sm text-slate-100">{key.name}</p>
							<p class="mt-1 text-xs text-slate-500">
								{m.settings_mcp_created_at({ date: moment(key.created_at) })}
							</p>
						</div>

						{#if mcpConfirming === key.id}
							<div class="flex flex-wrap items-center gap-3">
								<span class="text-sm text-slate-300">{m.settings_mcp_confirm()}</span>
								<button
									type="button"
									disabled={mcpRevoking === key.id}
									onclick={() => revokeMCPKey(key.id)}
									class="rounded bg-rose-500 px-3 py-1.5 text-sm font-medium text-white transition-colors hover:bg-rose-400 disabled:opacity-60"
								>
									{mcpRevoking === key.id ? m.settings_mcp_revoking() : m.settings_mcp_confirm_yes()}
								</button>
								<button
									type="button"
									onclick={() => (mcpConfirming = null)}
									class="rounded px-3 py-1.5 text-sm text-slate-300 transition-colors hover:text-white"
								>
									{m.settings_mcp_confirm_no()}
								</button>
							</div>
						{:else}
							<button
								type="button"
								onclick={() => (mcpConfirming = key.id)}
								class="rounded border border-slate-700 px-3 py-1.5 text-sm text-slate-300 transition-colors hover:border-rose-500 hover:text-rose-300"
							>
								{m.settings_mcp_revoke()}
							</button>
						{/if}
					</li>
				{/each}
			</ul>
		{/if}

		{#if mcpFailure}
			<p class="mt-4 text-sm text-rose-400" role="alert">{mcpFailure}</p>
		{/if}
	{/if}
</section>
