<script>
	import { m } from '../paraglide/messages.js'
	import { getLocale, setLocale } from '../paraglide/runtime.js'

	// Выбор языка переживает перезагрузку: Paraglide хранит его в cookie и сам
	// перезагружает страницу, чтобы сообщения взялись из нужного набора.
	const current = getLocale()

	function choose(locale) {
		if (locale !== current) {
			setLocale(locale)
		}
	}
</script>

<div
	class="flex items-center gap-x-0.5 rounded border border-slate-700 p-0.5 text-xs"
	role="group"
	aria-label={m.locale_switch_label()}
>
	{#each ['ru', 'en'] as locale (locale)}
		<button
			type="button"
			onclick={() => choose(locale)}
			aria-pressed={current === locale}
			class="rounded px-1.5 py-0.5 transition-colors {current === locale
				? 'bg-slate-800 text-white'
				: 'text-slate-400 hover:text-slate-100'}"
		>
			{locale.toUpperCase()}
		</button>
	{/each}
</div>
