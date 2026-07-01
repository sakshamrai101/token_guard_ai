package budget

import _ "embed"

//go:embed lua/reserve_budget.lua
var reserveBudgetLua string

//go:embed lua/release_budget.lua
var releaseBudgetLua string

//go:embed lua/settle_budget.lua
var settleBudgetLua string
