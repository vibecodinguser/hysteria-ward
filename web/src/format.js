// Форматирование чисел для интерфейса: байты, скорость и время.
//
// Считаем в двоичных единицах (1024): так объём показывают и браузеры, и
// роутеры, а «мегабайт» в панели должен означать то же, что в клиенте.

import { getLocale } from './paraglide/runtime.js'

const units = {
	ru: ['Б', 'КБ', 'МБ', 'ГБ', 'ТБ', 'ПБ'],
	en: ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
}

const perSecond = { ru: '/с', en: '/s' }
const intlLocales = { ru: 'ru-RU', en: 'en-US' }

/** Ключ языка, для которого есть единицы и формат даты. */
function localeKey() {
	return getLocale() === 'en' ? 'en' : 'ru'
}

/** Локаль Intl для выбранного языка. */
function intlLocale() {
	return intlLocales[localeKey()]
}

/**
 * Объём: «12,3 МБ». До килобайта — целые байты, дальше две значащие цифры после
 * единиц, чтобы столбец не дёргался на каждой сотой.
 */
export function formatBytes(bytes) {
	const scale = units[localeKey()]
	let value = Number(bytes) || 0
	let unit = 0

	while (value >= 1024 && unit < scale.length - 1) {
		value /= 1024
		unit += 1
	}

	const digits = unit === 0 ? 0 : value >= 100 ? 0 : value >= 10 ? 1 : 2
	const number = new Intl.NumberFormat(intlLocale(), { maximumFractionDigits: digits }).format(value)

	return number + ' ' + scale[unit]
}

/** Скорость: «12,3 МБ/с» или «12.3 MB/s». */
export function formatRate(bytesPerSecond) {
	return formatBytes(bytesPerSecond) + perSecond[localeKey()]
}

/** Байты в секунду в Мбит/с: так скорость задают в настройках. */
export function toMbps(bytesPerSecond) {
	const value = (Number(bytesPerSecond) || 0) * 8 / 1e6

	// Четыре знака, а не два: минимум ядра — 65536 байт/с, это 0,524288 Мбит/с.
	// После округления до сотых поле показало бы 0,52, и повторное «Применить»
	// отправило бы 65000 — меньше минимума.
	return Math.round(value * 10000) / 10000
}

/** Мбит/с в байты в секунду: ядро считает в байтах. */
export function fromMbps(mbps) {
	const value = Number(mbps)
	if (!Number.isFinite(value) || value <= 0) {
		return 0
	}

	return Math.round(value * 1e6 / 8)
}

/**
 * Время окна: часы и минуты, а для суток и недели ещё и дата — иначе на семи
 * сутках подписи были бы неразличимы.
 */
export function formatClock(at, withDate = false) {
	const date = new Date(at)

	const format = withDate
		? { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' }
		: { hour: '2-digit', minute: '2-digit' }

	return new Intl.DateTimeFormat(intlLocale(), format).format(date)
}

/** Дата и время события в выбранной локали. */
export function formatDateTime(value) {
	return new Date(value).toLocaleString(intlLocale())
}
