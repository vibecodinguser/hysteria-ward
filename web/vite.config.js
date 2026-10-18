import { paraglideVitePlugin } from '@inlang/paraglide-js'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vite'

// Собранный интерфейс ложится прямо в пакет, который вшивает его в бинарь:
// рядом с бинарём не появляется ни одного внешнего файла, и Node.js на сервере
// не нужен.
export default defineConfig({
	plugins: [
		paraglideVitePlugin({ project: './project.inlang', outdir: './src/paraglide' }),
		svelte(),
		tailwindcss()
	],
	build: {
		outDir: '../internal/ui/dist',
		emptyOutDir: true,
		// Имя файла содержит хеш содержимого, поэтому кэшировать его можно вечно.
		assetsDir: 'assets'
	}
})
