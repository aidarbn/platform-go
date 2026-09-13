package i18nx

// Builtin are the translations of the messages the platform itself answers with, and
// taply's generic patterns and messages. A project adds its own in i18n/messages.yaml.
const Builtin = `
error.pattern.missing: {en: "missing %s", ru: "отсутствует %s", kk: "%s жоқ"}
error.pattern.is_required: {en: "%s is required", ru: "%s обязательно", kk: "%s міндетті"}

error.msg.not found: {en: not found, ru: не найден, kk: табылмады}
error.msg.already exists: {en: already exists, ru: уже существует, kk: бұрыннан бар}
error.msg.rate limit exceeded: {en: rate limit exceeded, ru: превышен лимит запросов, kk: сұраныстар шегінен асып кетті}
error.msg.authentication required: {en: authentication required, ru: требуется аутентификация, kk: аутентификация қажет}
error.msg.request is already in progress: {en: request is already in progress, ru: запрос уже выполняется, kk: сұраныс орындалуда}
error.msg.duplicate request with different payload: {en: duplicate request with different payload, ru: повторный запрос с другими данными, kk: басқа деректермен қайталанған сұраныс}
error.msg.the call was canceled: {en: the call was canceled, ru: запрос отменён, kk: сұраныс тоқтатылды}
error.msg.the call took too long: {en: the call took too long, ru: запрос выполнялся слишком долго, kk: сұраныс тым ұзақ орындалды}

error.tmpl.insufficient_role: {en: "insufficient role for %s", ru: "недостаточно прав для %s", kk: "%s үшін құқық жеткіліксіз"}
error.tmpl.route_not_found: {en: "%s: route not found", ru: "%s: маршрут не найден", kk: "%s: бағыт табылмады"}
error.tmpl.idempotency_key_required: {en: "%s is required in the header", ru: "в заголовке требуется %s", kk: "тақырыпта %s қажет"}
`
