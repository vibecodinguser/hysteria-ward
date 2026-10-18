<script>
	import { m } from './paraglide/messages.js'
	import NotFound from './pages/NotFound.svelte'
	import { currentPath, followHistory } from './router.svelte.js'
	import { resolveSection, sections } from './routes.js'

	// Переходы слушаются один раз за жизнь страницы.
	followHistory()

	const section = $derived(resolveSection(currentPath()))
	const Page = $derived(section?.page ?? NotFound)
</script>

<div class="flex min-h-screen flex-col bg-slate-950 text-slate-100">
	<header class="border-b border-slate-800 bg-slate-900/60">
		<div class="mx-auto flex max-w-5xl flex-wrap items-center gap-x-8 gap-y-3 px-6 py-4">
			<span class="text-lg font-semibold tracking-tight">{m.app_title()}</span>
			<nav class="flex flex-wrap gap-x-1 gap-y-1 text-sm">
				{#each sections as item (item.path)}
					<a
						href={item.path}
						aria-current={item.path === currentPath() ? 'page' : undefined}
						class="rounded px-2 py-1 transition-colors {item.path === currentPath()
							? 'bg-slate-800 text-white'
							: 'text-slate-400 hover:text-slate-100'}"
					>
						{item.label()}
					</a>
				{/each}
			</nav>
		</div>
	</header>

	<main class="mx-auto w-full max-w-5xl flex-1 px-6 py-10">
		<Page />
	</main>
</div>
