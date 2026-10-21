<script>
	import LocaleSwitch from './components/LocaleSwitch.svelte'
	import { m } from './paraglide/messages.js'
	import Login from './pages/Login.svelte'
	import NotFound from './pages/NotFound.svelte'
	import { currentPath, followHistory } from './router.svelte.js'
	import { resolveSection, sections } from './routes.js'
	import { currentSession, isLoading, restore, signOut } from './session.svelte.js'

	// Переходы слушаются один раз за жизнь страницы.
	followHistory()

	// Вход спрашивается у панели тоже один раз: cookie живёт в браузере, а
	// знание о входе — в панели.
	restore()

	const session = $derived(currentSession())
	const section = $derived(resolveSection(currentPath()))
	const Page = $derived(section?.page ?? NotFound)
</script>

{#if isLoading()}
	<!-- Пока неизвестно, вошли ли, показывать нечего: показать панель и тут же
	     спрятать её за формой входа — хуже, чем подождать долю секунды. -->
	<div class="flex min-h-screen items-center justify-center bg-slate-950 text-slate-500">
		{m.session_loading()}
	</div>
{:else if !session}
	<Login />
{:else}
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
				<div class="ml-auto flex items-center gap-x-3 text-sm">
					<LocaleSwitch />
					<span class="text-slate-400">{session.name}</span>
					<button
						type="button"
						onclick={signOut}
						class="rounded px-2 py-1 text-slate-400 transition-colors hover:text-slate-100"
					>
						{m.session_logout()}
					</button>
				</div>
			</div>
		</header>

		<main class="mx-auto w-full max-w-5xl flex-1 px-6 py-10">
			<Page />
		</main>
	</div>
{/if}
