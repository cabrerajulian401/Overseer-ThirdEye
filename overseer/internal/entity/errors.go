package entity

import "errors"

// ErrReauthRequired marks Graph-token failures only the user can fix by
// signing in again (AAD's consent / interaction-required family). Controllers
// map it to a distinct error code so the FE can show "reconnect your mailbox"
// instead of a generic failure.
var ErrReauthRequired = errors.New("user interaction required to refresh Graph access")
