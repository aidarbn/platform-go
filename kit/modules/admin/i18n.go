package admin

// translations of the panel, keyed by the English text. A missing text stays English.
var translations = map[string]map[string]string{
	"ru": {
		"Sign in": "Вход", "Sign out": "Выйти", "Email": "Почта", "Password": "Пароль", "One time code": "Одноразовый код",
		"if enabled": "если включён", "Signed in as": "Вы вошли как", "roles": "роли", "Business settings": "Бизнес-настройки",
		"total": "всего", "changed": "изменено", "the settings module is off": "модуль настроек выключен", "Accounts": "Учётные записи",
		"Recent actions": "Последние действия", "The project has no business settings: describe them in settings.yaml.": "У проекта нет бизнес-настроек: опишите их в settings.yaml.",
		"Setting": "Настройка", "Value": "Значение", "Default": "По умолчанию", "applies after a restart": "применится после перезапуска",
		"Save": "Сохранить", "min": "мин.", "max": "макс.", "Reset": "Сбросить", "Roles": "Роли", "Two factor": "Второй фактор",
		"State": "Состояние", "Last sign in": "Последний вход", "on": "включён", "off": "выключен", "disabled": "отключена",
		"active": "активна", "never": "никогда", "Enable": "Включить", "Disable": "Отключить", "Delete": "Удалить",
		"Add an account": "Новая учётная запись", "email": "почта", "password": "пароль", "roles, comma separated": "роли через запятую",
		"two factor": "второй фактор", "Add": "Добавить",
		"the account is created. The secret is shown once: add it to an authenticator app now.": "учётная запись создана. Секрет показывается один раз: добавьте его в приложение-аутентификатор сейчас.",
		"Link for a QR code": "Ссылка для QR-кода", "Back to the accounts": "К учётным записям", "Older": "Раньше",
		"The page needs a role this account does not have.": "Для страницы нужна роль, которой у этой учётной записи нет.",
		"No records.": "Записей нет.", "When": "Когда", "Who": "Кто", "What": "Что", "Target": "Над чем", "Details": "Подробности",
		"Address": "Адрес", "Not enough rights": "Недостаточно прав", "Overview": "Обзор", "Settings": "Настройки", "Audit": "Журнал",
		"Two factor authentication": "Двухфакторная аутентификация", "Audit log": "Журнал действий", "%s saved": "%s сохранено",
		"%s back to the default": "%s: значение по умолчанию", "%s added": "%s добавлена", "%s enabled": "%s включена",
		"%s disabled": "%s отключена", "%s deleted": "%s удалена", "you cannot disable your own account": "нельзя отключить свою учётную запись",
		"you cannot delete your own account":                      "нельзя удалить свою учётную запись",
		"the form has expired, open the page again":               "форма устарела, откройте страницу заново",
		"The account needs a one time code.":                      "Для учётной записи нужен одноразовый код.",
		"This code has already been used: wait for the next one.": "Этот код уже использован: дождитесь следующего.",
		"Wrong one time code.":                                    "Неверный одноразовый код.", "The account is disabled.": "Учётная запись отключена.",
		"Wrong email or password.": "Неверная почта или пароль.",
	},
}

func (s *server) language() string {
	if s.cfg.Language == "" {
		return "en"
	}
	return s.cfg.Language
}

// t translates a text of the panel into its language.
func (s *server) t(text string) string {
	if translated, ok := translations[s.language()][text]; ok {
		return translated
	}
	return text
}
