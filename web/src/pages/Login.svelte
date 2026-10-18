<script>
	import { m } from '../paraglide/messages.js'
	import { signIn } from '../session.svelte.js'

	let name = $state('')
	let password = $state('')
	let failure = $state(null)
	let sending = $state(false)

	async function submit(event) {
		event.preventDefault()

		// Двойной щелчок не должен превращаться в два входа: каждый вход — это
		// проверка пароля, а она считается памятью.
		if (sending) {
			return
		}

		sending = true
		failure = await signIn(name, password)
		sending = false
	}
</script>

<main class="flex min-h-screen items-center justify-center bg-slate-950 px-6 py-10 text-slate-100">
	<form class="w-full max-w-sm" onsubmit={submit}>
		<h1 class="text-xl font-semibold">{m.login_title()}</h1>
		<p class="mt-2 text-sm text-slate-400">{m.login_hint()}</p>

		<label class="mt-6 block text-sm text-slate-300">
			{m.login_name()}
			<input
				bind:value={name}
				name="name"
				autocomplete="username"
				required
				class="mt-1 w-full rounded border border-slate-700 bg-slate-900 px-3 py-2 text-slate-100"
			/>
		</label>

		<label class="mt-4 block text-sm text-slate-300">
			{m.login_password()}
			<input
				bind:value={password}
				type="password"
				name="password"
				autocomplete="current-password"
				required
				class="mt-1 w-full rounded border border-slate-700 bg-slate-900 px-3 py-2 text-slate-100"
			/>
		</label>

		{#if failure}
			<p class="mt-4 text-sm text-rose-400" role="alert">{failure}</p>
		{/if}

		<button
			type="submit"
			disabled={sending}
			class="mt-6 w-full rounded bg-slate-100 px-3 py-2 font-medium text-slate-900 transition-colors hover:bg-white disabled:opacity-60"
		>
			{sending ? m.login_submitting() : m.login_submit()}
		</button>
	</form>
</main>
