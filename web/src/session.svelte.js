// Вход в панель: кто вошёл, как войти и как выйти.
//
// Состояние живёт здесь, а не в компоненте: о нём спрашивают и шапка, и страница
// входа, и страницы с данными. Сама сессия панели — это cookie, которую браузер
// прикладывает к запросам сам; здесь только знание о ней и csrf-токен.

import { m } from './paraglide/messages.js'

let session = $state(null)
let loading = $state(true)

/** Кто вошёл, или null, если никто. */
export function currentSession() {
	return session
}

/** Идёт ли первая проверка входа: пока она не кончилась, показывать нечего. */
export function isLoading() {
	return loading
}

/**
 * Спросить панель, помнит ли она вход. Вызывается один раз за загрузку страницы:
 * cookie лежит в браузере, а знание о входе — в панели.
 */
export async function restore() {
	try {
		const answer = await apiFetch('/api/session')
		session = answer.ok ? await answer.json() : null
	} catch {
		// Панель не отвечает: считаем, что вход не подтверждён, и показываем форму.
		// Врать администратору, что он вошёл, нельзя.
		session = null
	} finally {
		loading = false
	}
}

/**
 * Войти. Возвращает причину отказа, а при удаче — null.
 *
 * Причину называет панель, и показывается она как есть: придумывать своё
 * объяснение на каждую беду — значит расходиться с тем, что панель на самом деле
 * проверила.
 */
export async function signIn(name, password) {
	let answer
	try {
		answer = await apiFetch('/api/login', { method: 'POST', body: { name, password } })
	} catch {
		return m.session_unreachable()
	}

	if (!answer.ok) {
		return (await reasonOf(answer)) ?? m.session_unreachable()
	}

	session = await answer.json()

	return null
}

/** Выйти. */
export async function signOut() {
	try {
		await apiFetch('/api/logout', { method: 'POST' })
	} catch {
		// Выход не дошёл — возможно, сети уже нет. Сессию всё равно забываем:
		// оставлять открытым интерфейс, который ничего не прочитает, незачем.
	}

	session = null
}

/**
 * Запрос к панели.
 *
 * Живёт здесь, потому что изменяющие запросы уходят с csrf-токеном: без него
 * панель их отклоняет — этим и закрыт чужой сайт, который cookie пришлёт, а
 * заголовок подставить не сможет. Каждый, кто ходит в панель из интерфейса, идёт
 * через этот запрос, иначе токен рано или поздно забудут.
 */
export async function apiFetch(path, { method = 'GET', body } = {}) {
	const request = { method, headers: {} }

	if (body !== undefined) {
		request.headers['Content-Type'] = 'application/json'
		request.body = JSON.stringify(body)
	}

	if (session?.csrf && method !== 'GET' && method !== 'HEAD') {
		request.headers['X-CSRF-Token'] = session.csrf
	}

	return await fetch(path, request)
}

/** Причина отказа, если панель её объяснила. */
async function reasonOf(answer) {
	try {
		const { error } = await answer.json()

		return error || null
	} catch {
		return null
	}
}
