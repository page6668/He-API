package adapterclient

import "context"

// ctxAlias is a private alias for context.Context. It exists so the
// ClientHandle.Chat signature can declare a `contextLike` parameter
// without forcing every caller of Resolve to know about this package's
// internal naming.
type ctxAlias = context.Context
