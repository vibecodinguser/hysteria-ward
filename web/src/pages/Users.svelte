<script>
	import { m } from '../paraglide/messages.js'
	import { formatBytes } from '../format.js'
	import { apiFetch } from '../session.svelte.js'

	// Пользователи сервера: кому выдан ключ, кто включён и кто сейчас подключён.
	//
	// Список спрашивается у панели, а не запоминается: состояние меняется и с
	// другой вкладки, а показанное вчера — это не то, что есть сейчас.

	let users = $state([])
	let loading = $state(true)
	let failure = $state(null)

	// Заведение пользователя: имя вводит администратор, пароль придумывает панель.
	// Квота — в гигабайтах; пустое поле означает «без лимита».
	let newName = $state('')
	let newQuota = $state('')
	let creating = $state(false)

	// Ключ показывается по одному пользователю: это секрет, и держать их все
	// открытыми незачем.
	let keyFor = $state(null)
	let key = $state(null)
	let keyLoading = $state(false)
	let keyFailure = $state(null)
	let copied = $state(false)

	// keyVersion — время последней правки открытого пользователя: оно идёт в
	// адрес картинки, чтобы браузер не показал QR-код прежнего пароля после его
	// смены.
	const keyVersion = $derived(users.find((item) => item.id === keyFor)?.updated_at ?? '')

	// Переименование и удаление спрашивают ввод и подтверждение прямо в строке:
	// отдельное окно на каждое действие — это больше кода и больше промахов.
	let renaming = $state(null)
	let renameText = $state('')
	let deleting = $state(null)
	let sending = $state(false)

	// Квота правится в строке пользователя: инлайн-форма не уводит со страницы и
	// показывает текущий расход рядом с полем.
	let quotaFor = $state(null)
	let quotaText = $state('')

	load()

	async function load() {
		try {
			const answer = await apiFetch('/api/users')
			if (!answer.ok) {
				failure = m.users_failed()
				return
			}

			users = (await answer.json()).users
		} catch {
			failure = m.session_unreachable()
		} finally {
			loading = false
		}
	}

	// ask отправляет изменяющий запрос и перечитывает список: панель уже применила
	// правку, и показывать старое нельзя.
	async function ask(path, { method, body } = {}) {
		sending = true
		failure = null

		try {
			const answer = await apiFetch(path, { method, body })
			if (!answer.ok) {
				failure = (await reasonOf(answer)) ?? m.users_failed()
				return null
			}

			await load()

			return answer
		} catch {
			failure = m.session_unreachable()
			return null
		} finally {
			sending = false
		}
	}

	async function create() {
		if (creating) {
			return
		}

		creating = true
		failure = null

		try {
			const answer = await apiFetch('/api/users', {
				method: 'POST',
				body: { name: newName, quota_bytes: gibToBytes(newQuota) }
			})

			if (!answer.ok) {
				failure = (await reasonOf(answer)) ?? m.users_create_failed()
				return
			}

			const created = await answer.json()
			newName = ''
			newQuota = ''

			await load()

			// Ключ показывается сразу: его выдают тому, для кого завели пользователя.
			await showKey(created)
		} catch {
			failure = m.users_create_failed()
		} finally {
			creating = false
		}
	}

	async function toggle(user) {
		await ask('/api/users/' + encodeURIComponent(user.id), {
			method: 'PATCH',
			body: { enabled: !user.enabled }
		})

		// Выключенный не может быть подключён: панель уже порвала его соединения.
		if (keyFor === user.id) {
			await showKey(user)
		}
	}

	async function saveRename(user) {
		if (renameText.trim() === '') {
			return
		}

		const answer = await ask('/api/users/' + encodeURIComponent(user.id), {
			method: 'PATCH',
			body: { name: renameText }
		})

		if (answer) {
			renaming = null
		}
	}

	function startQuota(user) {
		quotaFor = user.id
		quotaText = user.quota_bytes > 0 ? String(roundQuota(user.quota_bytes)) : ''
	}

	async function saveQuota(user) {
		const answer = await ask('/api/users/' + encodeURIComponent(user.id), {
			method: 'PATCH',
			body: { quota_bytes: gibToBytes(quotaText) }
		})

		if (answer) {
			quotaFor = null
		}
	}

	async function resetQuota(user) {
		const answer = await ask('/api/users/' + encodeURIComponent(user.id) + '/quota/reset', {
			method: 'POST'
		})

		if (answer) {
			quotaFor = null
		}
	}

	// gibToBytes переводит поле в гигабайтах в байты. Пустое или отрицательное
	// значение — это ноль, то есть «без лимита».
	function gibToBytes(value) {
		const number = Number(value)
		if (!Number.isFinite(number) || number <= 0) {
			return 0
		}

		return Math.round(number * 1024 ** 3)
	}

	// roundQuota показывает предел в гигабайтах без хвоста из-за двоичного
	// пересчёта: администратор вводил гигабайты, их и видит.
	function roundQuota(bytes) {
		return Math.round((bytes / 1024 ** 3) * 100) / 100
	}

	async function remove(user) {
		const answer = await ask('/api/users/' + encodeURIComponent(user.id), { method: 'DELETE' })

		if (answer) {
			deleting = null
			if (keyFor === user.id) {
				closeKey()
			}
		}
	}

	async function showKey(user) {
		keyFor = user.id
		key = null
		keyFailure = null
		keyLoading = true
		copied = false

		try {
			const answer = await apiFetch('/api/users/' + encodeURIComponent(user.id) + '/key')
			if (!answer.ok) {
				// Причину называет панель: «укажите домен» — её знание, а не догадка
				// интерфейса.
				keyFailure = (await reasonOf(answer)) ?? m.users_key_failed()
				return
			}

			key = await answer.json()
		} catch {
			keyFailure = m.session_unreachable()
		} finally {
			keyLoading = false
		}
	}

	function closeKey() {
		keyFor = null
		key = null
		keyFailure = null
	}

	async function newPassword() {
		const answer = await ask('/api/users/' + encodeURIComponent(keyFor) + '/password', {
			method: 'POST'
		})

		if (answer) {
			await refreshKey()
		}
	}

	// refreshKey перечитывает ключ пользователя, открытого в панели.
	async function refreshKey() {
		const user = users.find((item) => item.id === keyFor)
		if (user) {
			await showKey(user)
		}
	}

	async function copyKey() {
		try {
			await navigator.clipboard.writeText(key.uri)
			copied = true
		} catch {
			// Буфер обмена может быть закрыт браузером. Ключ показан целиком, и его
			// можно выделить руками — молчать об этом хуже, чем сказать.
			copied = false
			keyFailure = m.users_copy_failed()
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

<h1 class="text-xl font-semibold">{m.nav_users()}</h1>
<p class="mt-3 text-slate-400">{m.users_hint()}</p>

{#if failure}
	<p class="mt-4 text-sm text-rose-400" role="alert">{failure}</p>
{/if}

<section class="mt-8 rounded border border-slate-800 bg-slate-900/40 p-6">
	<h2 class="font-semibold">{m.users_create_title()}</h2>

	<form
		class="mt-4 flex flex-wrap gap-3"
		onsubmit={(event) => {
			event.preventDefault()
			create()
		}}
	>
		<input
			type="text"
			bind:value={newName}
			placeholder={m.users_create_placeholder()}
			aria-label={m.users_name()}
			class="min-w-56 flex-1 rounded border border-slate-700 bg-slate-950 px-3 py-2 text-sm text-slate-100 placeholder:text-slate-600"
		/>
		<label class="flex items-center gap-2 text-sm text-slate-300">
			<span>{m.users_quota_gb()}</span>
			<input
				type="number"
				min="0"
				step="any"
				bind:value={newQuota}
				placeholder="0"
				aria-label={m.users_quota_gb()}
				class="w-24 rounded border border-slate-700 bg-slate-950 px-3 py-2 text-sm text-slate-100 placeholder:text-slate-600"
			/>
		</label>
		<button
			type="submit"
			disabled={creating}
			class="rounded bg-slate-100 px-3 py-2 text-sm font-medium text-slate-900 transition-colors hover:bg-white disabled:opacity-60"
		>
			{creating ? m.users_creating() : m.users_create_submit()}
		</button>
	</form>
</section>

<section class="mt-6 rounded border border-slate-800 bg-slate-900/40">
	{#if loading}
		<p class="p-6 text-sm text-slate-400">{m.users_loading()}</p>
	{:else if users.length === 0}
		<p class="p-6 text-sm text-slate-400">{m.users_empty()}</p>
	{:else}
		<ul>
			{#each users as user (user.id)}
				<li class="border-b border-slate-800 p-6 last:border-b-0">
					<div class="flex flex-wrap items-center gap-x-4 gap-y-3">
						{#if renaming === user.id}
							<form
								class="flex flex-1 flex-wrap gap-2"
								onsubmit={(event) => {
									event.preventDefault()
									saveRename(user)
								}}
							>
								<input
									type="text"
									bind:value={renameText}
									aria-label={m.users_rename_title()}
									class="min-w-48 flex-1 rounded border border-slate-700 bg-slate-950 px-3 py-1.5 text-sm text-slate-100"
								/>
								<button
									type="submit"
									disabled={sending}
									class="rounded bg-slate-100 px-3 py-1.5 text-sm font-medium text-slate-900 transition-colors hover:bg-white disabled:opacity-60"
								>
									{m.users_rename_save()}
								</button>
								<button
									type="button"
									onclick={() => (renaming = null)}
									class="rounded px-3 py-1.5 text-sm text-slate-300 transition-colors hover:text-white"
								>
									{m.users_rename_cancel()}
								</button>
							</form>
						{:else}
							<button
								type="button"
								onclick={() => showKey(user)}
								class="text-left font-medium text-slate-100 transition-colors hover:text-white"
							>
								{user.name}
							</button>

							<span
								class="rounded px-2 py-0.5 text-xs {user.enabled
									? 'bg-emerald-950 text-emerald-300'
									: 'bg-slate-800 text-slate-400'}"
							>
								{user.enabled ? m.users_enabled() : m.users_disabled()}
							</span>

							{#if user.online}
								<span class="rounded bg-sky-950 px-2 py-0.5 text-xs text-sky-300">
									{m.users_online()}
								</span>
							{/if}

							{#if user.quota_bytes > 0}
								<span
									class="rounded px-2 py-0.5 text-xs {user.quota_exhausted
										? 'bg-rose-950 text-rose-300'
										: 'bg-slate-800 text-slate-300'}"
								>
									{m.users_quota_usage({
										used: formatBytes(user.quota_used),
										total: formatBytes(user.quota_bytes)
									})}
								</span>
								{#if user.quota_exhausted}
									<span class="rounded bg-rose-950 px-2 py-0.5 text-xs text-rose-300">
										{m.users_quota_exhausted()}
									</span>
								{/if}
							{:else}
								<span class="rounded bg-slate-800 px-2 py-0.5 text-xs text-slate-400">
									{m.users_quota_unlimited()}
								</span>
							{/if}

							<div class="ml-auto flex flex-wrap gap-2 text-sm">
								<button
									type="button"
									onclick={() => showKey(user)}
									class="rounded border border-slate-700 px-3 py-1.5 text-slate-200 transition-colors hover:border-slate-500 hover:text-white"
								>
									{m.users_key()}
								</button>
								<button
									type="button"
									onclick={() => startQuota(user)}
									class="rounded border border-slate-700 px-3 py-1.5 text-slate-200 transition-colors hover:border-slate-500 hover:text-white"
								>
									{m.users_quota_edit()}
								</button>
								<button
									type="button"
									disabled={sending}
									onclick={() => toggle(user)}
									class="rounded border border-slate-700 px-3 py-1.5 text-slate-200 transition-colors hover:border-slate-500 hover:text-white disabled:opacity-60"
								>
									{user.enabled ? m.users_disable() : m.users_enable()}
								</button>
								<button
									type="button"
									onclick={() => {
										renaming = user.id
										renameText = user.name
									}}
									class="rounded px-3 py-1.5 text-slate-400 transition-colors hover:text-white"
								>
									{m.users_rename()}
								</button>
								{#if deleting === user.id}
									<button
										type="button"
										disabled={sending}
										onclick={() => remove(user)}
										class="rounded bg-rose-500 px-3 py-1.5 font-medium text-slate-950 transition-colors hover:bg-rose-400 disabled:opacity-60"
									>
										{m.users_delete_confirm()}
									</button>
									<button
										type="button"
										onclick={() => (deleting = null)}
										class="rounded px-3 py-1.5 text-slate-300 transition-colors hover:text-white"
									>
										{m.users_delete_cancel()}
									</button>
								{:else}
									<button
										type="button"
										onclick={() => (deleting = user.id)}
										class="rounded px-3 py-1.5 text-slate-400 transition-colors hover:text-rose-300"
									>
										{m.users_delete()}
									</button>
								{/if}
							</div>
						{/if}
					</div>

					{#if quotaFor === user.id}
						<form
							class="mt-4 flex flex-wrap items-end gap-3 rounded border border-slate-800 bg-slate-950/50 p-4"
							onsubmit={(event) => {
								event.preventDefault()
								saveQuota(user)
							}}
						>
							<label class="flex flex-col gap-1 text-sm text-slate-300">
								<span>{m.users_quota_edit_title()}</span>
								<input
									type="number"
									min="0"
									step="any"
									bind:value={quotaText}
									placeholder="0"
									class="w-32 rounded border border-slate-700 bg-slate-950 px-3 py-2 text-sm text-slate-100 placeholder:text-slate-600"
								/>
							</label>
							<span class="text-xs text-slate-500">{m.users_quota_gb_hint()}</span>
							<button
								type="submit"
								disabled={sending}
								class="rounded bg-slate-100 px-3 py-2 text-sm font-medium text-slate-900 transition-colors hover:bg-white disabled:opacity-60"
							>
								{m.users_quota_save()}
							</button>
							{#if user.quota_bytes > 0}
								<button
									type="button"
									disabled={sending}
									onclick={() => resetQuota(user)}
									class="rounded border border-slate-700 px-3 py-2 text-sm text-slate-200 transition-colors hover:border-slate-500 hover:text-white disabled:opacity-60"
								>
									{m.users_quota_reset()}
								</button>
							{/if}
							<button
								type="button"
								onclick={() => (quotaFor = null)}
								class="rounded px-3 py-2 text-sm text-slate-400 transition-colors hover:text-white"
							>
								{m.users_quota_cancel()}
							</button>
						</form>
					{/if}
				</li>
			{/each}
		</ul>
	{/if}
</section>

{#if keyFor}
	<section class="mt-6 rounded border border-slate-800 bg-slate-900/40 p-6">
		<div class="flex flex-wrap items-center gap-x-4">
			<h2 class="font-semibold">{m.users_key_title()}</h2>
			<button
				type="button"
				onclick={closeKey}
				class="ml-auto rounded px-3 py-1.5 text-sm text-slate-400 transition-colors hover:text-white"
			>
				{m.users_key_hide()}
			</button>
		</div>

		{#if keyLoading}
			<p class="mt-4 text-sm text-slate-400">{m.users_key_loading()}</p>
		{:else if keyFailure}
			<p class="mt-4 text-sm text-rose-400" role="alert">{keyFailure}</p>
		{:else if key}
			<div class="mt-4 flex flex-wrap gap-8">
				<!-- Отпечаток сертификата берётся панелью из того же сертификата, что
				     она сама отдаёт, поэтому картинка — это тот же ключ, что ниже. -->
				<img
					src={'/api/users/' + encodeURIComponent(keyFor) + '/qr?v=' + encodeURIComponent(keyVersion)}
					alt={m.users_qr_alt()}
					width="224"
					height="224"
					class="h-56 w-56 rounded bg-white p-2"
				/>

				<div class="min-w-64 flex-1">
					<p class="text-sm text-slate-400">{m.users_key_hint()}</p>

					<textarea
						readonly
						rows="6"
						class="mt-3 w-full break-all rounded border border-slate-700 bg-slate-950 p-3 font-mono text-xs text-slate-200"
					>{key.uri}</textarea>

					<div class="mt-4 flex flex-wrap gap-2">
						<button
							type="button"
							onclick={copyKey}
							class="rounded bg-slate-100 px-3 py-2 text-sm font-medium text-slate-900 transition-colors hover:bg-white"
						>
							{copied ? m.users_key_copied() : m.users_key_copy()}
						</button>
						<button
							type="button"
							disabled={sending}
							onclick={newPassword}
							class="rounded border border-slate-700 px-3 py-2 text-sm text-slate-200 transition-colors hover:border-slate-500 hover:text-white disabled:opacity-60"
						>
							{m.users_new_password()}
						</button>
					</div>

					<p class="mt-3 text-xs text-slate-500">{m.users_new_password_hint()}</p>
				</div>
			</div>
		{/if}
	</section>
{/if}
