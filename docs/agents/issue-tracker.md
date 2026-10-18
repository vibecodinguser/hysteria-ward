# Трекер задач: GitHub

Задачи и спецификации этого репозитория живут в GitHub Issues. Все операции выполняются через CLI `gh`.

## Соглашения

- **Создать задачу**: `gh issue create --title "..." --body "..."`. Для многострочного тела используй heredoc.
- **Прочитать задачу**: `gh issue view <number> --json number,title,body,labels,comments`.
- **Список задач**: `gh issue list --state open --json number,title,body,labels,comments --jq '[.[] | {number, title, body, labels: [.labels[].name], comments: [.comments[].body]}]'` с нужными фильтрами `--label` и `--state`.
- **Сделать задачу подзадачей родителя**: `gh issue create --parent <parent> ...` либо `gh issue edit <parent> --add-sub-issue <child>` (`gh` 2.94+). На старых версиях `gh`: `gh api --method POST repos/<owner>/<repo>/issues/<parent>/sub_issues -F sub_issue_id=<child-db-id>` (числовой database id, как в разделе «Блокировки» ниже). Если подзадачи недоступны, поставь `Part of #<parent>` в начале тела задачи.
- **Прокомментировать задачу**: `gh issue comment <number> --body "..."`
- **Поставить / снять метки**: `gh issue edit <number> --add-label "..."` / `--remove-label "..."`
- **Закрыть**: `gh issue close <number> --comment "..."`

Репозиторий выводится из `git remote -v`; внутри клона `gh` определяет его сам.

## Pull request'ы как поверхность триажа

**PR как поверхность запросов: нет.** _(Поставь `yes`, если репозиторий считает внешние PR запросами на фичу; `/triage` читает этот флаг.)_

Когда флаг равен `yes`, PR проходят те же метки и состояния, что и задачи, через аналоги из `gh pr`:

- **Прочитать PR**: `gh pr view <number> --comments` и `gh pr diff <number>` для диффа.
- **Список внешних PR для триажа**: `gh api --paginate 'repos/{owner}/{repo}/pulls?state=open' --jq '.[] | select(.author_association | IN("OWNER","MEMBER","COLLABORATOR") | not) | {number, title, author: .user.login, author_association, labels: [.labels[].name]}'`.
- **Комментарий / метка / закрытие**: `gh pr comment`, `gh pr edit --add-label`/`--remove-label`, `gh pr close`.

Нумерация у задач и PR общая, поэтому `#42` может оказаться и тем и другим: проверь `gh pr view 42` и откатись к `gh issue view 42`.

## Когда скилл говорит «опубликовать в трекер»

Создай GitHub issue.

## Когда скилл говорит «достать соответствующий тикет»

Читай так же, как в «Прочитать задачу» выше.

## Операции wayfinding

Используется скиллом `/wayfinder`. **Карта** — это одна задача с дочерними задачами-тикетами.

- **Карта**: одна задача с меткой `wayfinder:map`, тело которой хранит Notes / Decisions-so-far / Fog. `gh issue create --label wayfinder:map`.
- **Дочерний тикет**: задача, привязанная к карте как GitHub sub-issue (см. «Сделать задачу подзадачей родителя»). Где подзадачи не включены, добавь ребёнка в task list тела карты и поставь `Part of #<map>` в начале тела ребёнка. Метки: `wayfinder:<type>` (`research`/`prototype`/`grilling`/`task`). После захвата тикет назначается ведущему разработчику.
- **Блокировки**: **нативные зависимости** GitHub — каноническое, видимое в интерфейсе представление. Ребро добавляется так: `gh api --method POST repos/<owner>/<repo>/issues/<child>/dependencies/blocked_by -F issue_id=<blocker-db-id>`, где `<blocker-db-id>` — числовой **database id** блокирующей задачи (`gh api repos/<owner>/<repo>/issues/<n> --jq .id`, _а не_ `#number` или `node_id`). GitHub отдаёт `issue_dependencies_summary.blocked_by` (только открытые блокировщики — живой гейт). Где зависимости недоступны, откатывайся к строке `Blocked by: #<n>, #<n>` в начале тела ребёнка. Тикет разблокирован, когда каждая блокирующая его задача закрыта.
- **Запрос фронтира**: перечисли открытых детей карты (`gh issue list --state open`, ограничив подзадачами / task list карты), выбрось тех, у кого есть открытый блокировщик (`issue_dependencies_summary.blocked_by > 0` либо открытая задача в строке `Blocked by`), и тех, у кого есть исполнитель; первым побеждает идущий раньше по порядку карты.
- **Захват**: `gh issue edit <n> --add-assignee @me` — первая запись сессии.
- **Разрешение**: `gh issue comment <n> --body "<ответ>"`, затем `gh issue close <n>`, затем добавь указатель в Decisions-so-far карты (суть + ссылка).
