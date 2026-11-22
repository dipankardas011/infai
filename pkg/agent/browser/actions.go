package browser

// Action is one operation the extension can execute. The daemon and the
// extension agree on these names; typed constants keep a typo out of the wire.
type Action string

const (
	ActionNavigate       Action = "navigate"
	ActionSnapshot       Action = "snapshot"
	ActionScreenshot     Action = "screenshot"
	ActionScroll         Action = "scroll"
	ActionRead           Action = "read"
	ActionWaitFor        Action = "wait_for"
	ActionClick          Action = "click"
	ActionType           Action = "type"
	ActionSelect         Action = "select"
	ActionClickAt        Action = "click_at"
	ActionTypeAt         Action = "type_at"
	ActionHighlight      Action = "highlight"
	ActionClearHighlight Action = "clear_highlight"
	ActionReleaseSession Action = "release_session"
)
