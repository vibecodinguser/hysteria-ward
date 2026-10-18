import { m } from './paraglide/messages.js'
import Logs from './pages/Logs.svelte'
import Overview from './pages/Overview.svelte'
import Settings from './pages/Settings.svelte'
import Users from './pages/Users.svelte'

// Разделы панели: один список и для меню, и для разбора адреса — иначе они
// разъедутся при первом же новом разделе.
export const sections = [
	{ path: '/', label: m.nav_overview, page: Overview },
	{ path: '/users', label: m.nav_users, page: Users },
	{ path: '/settings', label: m.nav_settings, page: Settings },
	{ path: '/logs', label: m.nav_logs, page: Logs }
]

/**
 * Ищет раздел по адресу. Неизвестный адрес — не ошибка: интерфейс просто
 * показывает, что такой страницы нет.
 */
export function resolveSection(path) {
	// Хвостовой слэш не должен превращать известный раздел в «не найдено».
	const clean = path.length > 1 ? path.replace(/\/+$/, '') : path

	return sections.find((section) => section.path === clean) ?? null
}
